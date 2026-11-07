package releaseevidence

import (
	"bytes"
	"strings"
	"testing"
)

func TestParserStrictEvents(t *testing.T) {
	c := testCommand(Defaults("smoke"), backend, "test", faults[0])
	var valid bytes.Buffer
	passing(c, &valid)
	cases := map[string]string{
		"valid":            valid.String(),
		"duplicate":        valid.String() + valid.String(),
		"foreign":          strings.ReplaceAll(valid.String(), c.Package, "foreign/package"),
		"missing-top-pass": strings.Replace(valid.String(), `"Action":"pass","Package":"`+c.Package+`","Test":"`+faults[0]+`"`, `"Action":"output","Package":"`+c.Package+`","Test":"`+faults[0]+`"`, 1),
		"unknown-action":   strings.Replace(valid.String(), `"Action":"run"`, `"Action":"unknown"`, 1),
		"truncated":        strings.TrimSuffix(valid.String(), "\n"),
		"line-cap":         strings.Repeat("x", 65537),
		"non-object":       "[]\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := newParser(c)
			for len(data) > 0 {
				n := min(7, len(data))
				_, err := p.Write([]byte(data[:n]))
				data = data[n:]
				if err != nil {
					break
				}
			}
			err := p.finish()
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected parser outcome: %v", err)
			}
		})
	}
}
func TestNestedStreamCompleteness(t *testing.T) {
	c := testCommand(Defaults("smoke"), backend, "test", faults[0])
	for _, mode := range []string{"complete", "missing-child-pass", "orphan-child", "duplicate-child"} {
		t.Run(mode, func(t *testing.T) {
			p := newParser(c)
			event(p, c, "start", "", "")
			if mode != "orphan-child" {
				event(p, c, "run", faults[0], "")
			}
			event(p, c, "run", faults[0]+"/child", "")
			if mode != "missing-child-pass" {
				event(p, c, "pass", faults[0]+"/child", "")
			}
			if mode == "duplicate-child" {
				event(p, c, "pass", faults[0]+"/child", "")
			}
			event(p, c, "pass", faults[0], "")
			event(p, c, "pass", "", "")
			if err := p.finish(); (err == nil) != (mode == "complete") {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestBenchmarkExplicitMetricsOnly(t *testing.T) {
	c := benchmarkCommand(Defaults("long"))
	for name, line := range map[string]string{
		"valid":                    benchmark + "-8 1 2000 ns/op 2148.70 MB/s 168208 B/op 63 allocs/op\n",
		"missing":                  "",
		"only-announcement":        benchmark + "\n",
		"nan":                      benchmark + "-8 1 NaN ns/op 1 B/op 1 allocs/op\n",
		"inferred-bytes-forbidden": benchmark + "-8 1 2000 ns/op 1 allocs/op\n",
		"bad-iterations":           benchmark + "-8 2 2000 ns/op 1 B/op 1 allocs/op\n",
		"unknown-unit":             benchmark + "-8 1 2000 ns/op 1 B/op 1 allocs/op 4 gibberish\n",
		"duplicate-unit":           benchmark + "-8 1 2000 ns/op 1 B/op 1 allocs/op 4 ns/op\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := newParser(c)
			event(p, c, "start", "", "")
			if line != "" {
				event(p, c, "output", "", line)
			}
			event(p, c, "pass", "", "")
			err := p.finish()
			if (err == nil) != (name == "valid") {
				t.Fatalf("%v", err)
			}
			if name == "valid" {
				for _, metric := range p.metrics {
					if metric.Unit == "decoded-bytes/op" {
						t.Fatal("fabricated unobserved custom metric")
					}
				}
			}
		})
	}
}
func TestRSSRequiresObservedPairedMetric(t *testing.T) {
	c := testCommand(Defaults("long"), backend, "rss", rss)
	for _, line := range []string{"", "decoded evidence MaxRSS: 16MiB=0 bytes, 256MiB=12 bytes\n", "decoded evidence MaxRSS: 16MiB=12 bytes, 256MiB=30 bytes trailing\n", "decoded evidence MaxRSS: 16MiB=12 bytes, 256MiB=30 bytes\n"} {
		p := newParser(c)
		event(p, c, "start", "", "")
		event(p, c, "run", rss, "")
		event(p, c, "output", rss, line)
		event(p, c, "pass", rss, "")
		event(p, c, "pass", "", "")
		err := p.finish()
		want := strings.HasSuffix(line, "256MiB=30 bytes\n")
		if (err == nil) != want {
			t.Fatalf("line%q error%v", line, err)
		}
	}
}
