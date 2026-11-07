package releaseevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Metric struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
	Scope string  `json:"scope"`
}
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// eventParser processes complete JSON lines without retaining the stream.
type eventParser struct {
	command          Command
	pending          []byte
	started          map[string]bool
	passed           map[string]bool
	packageStarted   bool
	packagePassed    bool
	metrics          []Metric
	benchmarkLines   int
	benchmarkPending string
	rssObserved      bool
	err              error
}

func newParser(c Command) *eventParser {
	return &eventParser{command: c, started: map[string]bool{}, passed: map[string]bool{}}
}
func (p *eventParser) Write(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	for _, v := range b {
		if len(p.pending) >= 65536 {
			p.err = fmt.Errorf("JSON event line exceeds 65536 bytes")
			return 0, p.err
		}
		p.pending = append(p.pending, v)
		if v == '\n' {
			p.err = p.line(bytes.TrimSpace(p.pending))
			p.pending = p.pending[:0]
			if p.err != nil {
				return 0, p.err
			}
		}
	}
	return len(b), nil
}
func (p *eventParser) line(line []byte) error {
	var e testEvent
	if len(line) == 0 || json.Unmarshal(line, &e) != nil || e.Action == "" || e.Package != p.command.Package {
		return fmt.Errorf("malformed or foreign go test JSON event")
	}
	if p.packagePassed {
		return fmt.Errorf("event after package completion")
	}
	switch e.Action {
	case "start", "run", "pause", "cont", "output", "pass", "bench":
	case "skip", "fail", "build-fail":
		return fmt.Errorf("rejected %s event for %s", e.Action, e.Test)
	default:
		return fmt.Errorf("unknown go test action %q", e.Action)
	}
	if !p.packageStarted && !(e.Test == "" && e.Action == "start") {
		return fmt.Errorf("event before package start")
	}
	if e.Test == "" {
		switch e.Action {
		case "start":
			if p.packageStarted {
				return fmt.Errorf("duplicate package start")
			}
			p.packageStarted = true
		case "pass":
			if !p.packageStarted {
				return fmt.Errorf("package pass without start")
			}
			p.packagePassed = true
		case "output":
			if p.command.Benchmark {
				if err := p.bench(e.Output); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid package event %q", e.Action)
		}
		return nil
	}
	// Every started child must terminate too: package/top-level success cannot
	// mask a truncated or internally inconsistent nested stream.
	top, _, nested := strings.Cut(e.Test, "/")
	if nested {
		if !p.started[top] || p.passed[top] {
			return fmt.Errorf("child event without active top-level parent")
		}
		if e.Action == "run" {
			if p.started[e.Test] {
				return fmt.Errorf("duplicate child run")
			}
			p.started[e.Test] = true
		} else if !p.started[e.Test] || p.passed[e.Test] {
			return fmt.Errorf("child event without active run")
		}
		if e.Action == "pass" {
			p.passed[e.Test] = true
		}
		return nil
	}
	expected := false
	for _, n := range p.command.Expected {
		if n == e.Test {
			expected = true
		}
	}
	isBench := p.command.Benchmark && e.Test == benchmark
	if !expected && !isBench {
		return fmt.Errorf("unexpected top-level test %q", e.Test)
	}
	if e.Action == "run" {
		if p.started[e.Test] {
			return fmt.Errorf("duplicate test run")
		}
		p.started[e.Test] = true
	}
	if e.Action == "pass" {
		if !p.started[e.Test] || p.passed[e.Test] {
			return fmt.Errorf("test pass without unique run")
		}
		p.passed[e.Test] = true
	}
	if e.Action == "output" && p.command.Benchmark {
		return p.bench(e.Output)
	}
	if e.Action == "output" && e.Test == rss && strings.Contains(e.Output, "decoded evidence MaxRSS:") {
		const marker = "decoded evidence MaxRSS: "
		_, text, _ := strings.Cut(e.Output, marker)
		var short, long uint64
		if n, err := fmt.Sscanf(strings.TrimSpace(text), "16MiB=%d bytes, 256MiB=%d bytes", &short, &long); err != nil || n != 2 || short == 0 || long == 0 || p.rssObserved || strings.TrimSpace(text) != fmt.Sprintf("16MiB=%d bytes, 256MiB=%d bytes", short, long) {
			return fmt.Errorf("invalid RSS measurement")
		}
		p.rssObserved = true
		p.metrics = append(p.metrics, Metric{float64(short), "bytes", "synthetic reader subprocess MaxRSS, 16MiB input"}, Metric{float64(long), "bytes", "synthetic reader subprocess MaxRSS, 256MiB input"})
	}
	return nil
}
func (p *eventParser) bench(output string) error {
	p.benchmarkPending += output
	if len(p.benchmarkPending) > 65536 {
		return fmt.Errorf("benchmark output line cap exceeded")
	}
	for {
		line, rest, found := strings.Cut(p.benchmarkPending, "\n")
		if !found {
			return nil
		}
		p.benchmarkPending = rest
		if err := p.benchLine(line); err != nil {
			return err
		}
	}
}
func (p *eventParser) benchLine(output string) error {
	fields := strings.Fields(output)
	if len(fields) == 0 || !(fields[0] == benchmark || strings.HasPrefix(fields[0], benchmark+"-")) {
		return nil
	}
	// A benchmark announcement has no measurements. Never fabricate metrics from
	// configured stream size, elapsed time, or a prior machine's results.
	if len(fields) == 1 {
		return nil
	}
	if len(fields) < 4 || len(fields)%2 != 0 {
		return fmt.Errorf("malformed benchmark measurement")
	}
	iterations, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || iterations != 1 {
		return fmt.Errorf("benchmark iteration count must be one")
	}
	if p.benchmarkLines != 0 {
		return fmt.Errorf("duplicate benchmark measurement")
	}
	seen := map[string]bool{}
	for i := 2; i < len(fields); i += 2 {
		value, err := strconv.ParseFloat(fields[i], 64)
		unit := fields[i+1]
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || seen[unit] {
			return fmt.Errorf("invalid benchmark metric")
		}
		switch unit {
		case "ns/op", "MB/s", "B/op", "allocs/op", "decoded-bytes/op":
		default:
			return fmt.Errorf("unknown benchmark metric unit %q", unit)
		}
		seen[unit] = true
		p.metrics = append(p.metrics, Metric{Value: value, Unit: unit, Scope: "synthetic streaming decoder; observed explicit metric only"})
	}
	if !seen["ns/op"] || !seen["B/op"] || !seen["allocs/op"] {
		return fmt.Errorf("benchmark lacks required explicit metrics")
	}
	p.benchmarkLines++
	return nil
}
func (p *eventParser) finish() error {
	if p.err != nil {
		return p.err
	}
	if len(p.pending) != 0 {
		return fmt.Errorf("truncated JSON line")
	}
	if !p.packagePassed {
		return fmt.Errorf("missing package pass")
	}
	for name := range p.started {
		if p.command.Benchmark && name == benchmark {
			continue
		}
		if !p.passed[name] {
			return fmt.Errorf("missing terminal pass for started test %s", name)
		}
	}
	for _, n := range p.command.Expected {
		if !p.passed[n] {
			return fmt.Errorf("missing expected test pass: %s", n)
		}
		if n == rss && !p.rssObserved {
			return fmt.Errorf("missing required RSS measurement")
		}
	}
	if p.command.Benchmark && p.benchmarkLines != 1 {
		return fmt.Errorf("missing benchmark measurement")
	}
	return nil
}
