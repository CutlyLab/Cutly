package releaseevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"observed_path"`
	Version string `json:"observed_version"`
	SHA256  string `json:"observed_sha256"`
}
type Source struct {
	Revision        string    `json:"revision"`
	Dirty           bool      `json:"dirty"`
	DirtyStatus     string    `json:"dirty_status"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	Platform        string    `json:"platform"`
	RunnerToolchain string    `json:"runner_toolchain"`
	Tools           []Tool    `json:"tools"`
}
type Progress struct {
	FaultRoundsRequested    int           `json:"fault_rounds_requested"`
	FaultRoundsCompleted    int           `json:"fault_rounds_completed"`
	RecoveryRoundsRequested int           `json:"recovery_rounds_requested"`
	RecoveryRoundsCompleted int           `json:"recovery_rounds_completed"`
	ObservationRequested    time.Duration `json:"observation_requested_ns"`
	ObservationCompleted    time.Duration `json:"observation_completed_ns"`
	IntervalRequested       time.Duration `json:"interval_requested_ns"`
	IntervalsCompleted      int           `json:"intervals_completed"`
	IntervalCompleted       time.Duration `json:"interval_completed_ns"`
	RealMediaRunsRequested  int           `json:"real_media_runs_requested"`
	RealMediaRunsCompleted  int           `json:"real_media_runs_completed"`
}
type Invocation struct {
	Command      Command       `json:"command"`
	Status       string        `json:"status"`
	ExitCode     int           `json:"exit_code"`
	Error        string        `json:"error,omitempty"`
	Duration     time.Duration `json:"duration_ns"`
	Log          string        `json:"stdout"`
	Stderr       string        `json:"stderr"`
	LogTruncated bool          `json:"capture_truncated"`
	Metrics      []Metric      `json:"metrics,omitempty"`
}
type Report struct {
	Schema            string        `json:"schema"`
	Profile           string        `json:"profile"`
	CommandTimeout    time.Duration `json:"command_timeout_ns"`
	Budget            time.Duration `json:"overall_budget_ns"`
	LogBytesPerStream int           `json:"log_bytes_per_stream"`
	Scope             string        `json:"scope"`
	Status            string        `json:"status"`
	Error             string        `json:"error,omitempty"`
	Started           time.Time     `json:"started"`
	Finished          time.Time     `json:"finished"`
	Source            Source        `json:"source"`
	Progress          Progress      `json:"progress"`
	Commands          []Invocation  `json:"commands"`
	Limitations       []string      `json:"limitations"`
}

// Observe records local tool observations, not approval, signature verification,
// provenance guarantees, or an attestation of the exact binaries tests later use.
func Observe(ctx context.Context, c Config, e Executor) (Source, error) {
	s := Source{Platform: runtime.GOOS + "/" + runtime.GOARCH, RunnerToolchain: runtime.Version()}
	run := func(path string, args ...string) (string, error) {
		child, cancel := context.WithTimeout(ctx, min(c.CommandTimeout, 10*time.Second))
		defer cancel()
		out := &capture{limit: 65536, max: 65536, cancel: cancel}
		errout := &capture{limit: 4096, max: 65536, cancel: cancel}
		result := e.Execute(child, Command{Path: path, Args: args, Dir: c.Repo}, out, errout)
		if child.Err() != nil {
			return "", fmt.Errorf("observe %s: %w", path, child.Err())
		}
		if result.Err != nil {
			return "", fmt.Errorf("observe %s: exit %d: %w: %s", path, result.ExitCode, result.Err, errout.data)
		}
		if result.ExitCode != 0 {
			return "", fmt.Errorf("observe %s: exit %d: %s", path, result.ExitCode, errout.data)
		}
		if out.total > out.max || errout.total > errout.max {
			return "", fmt.Errorf("observation output cap exceeded")
		}
		return strings.TrimSpace(string(out.data)), nil
	}
	var err error
	if s.Revision, err = run("git", "rev-parse", "--verify", "HEAD"); err != nil {
		return s, err
	}
	if len(s.Revision) != 40 && len(s.Revision) != 64 {
		return s, fmt.Errorf("invalid observed revision")
	}
	if _, err = hex.DecodeString(s.Revision); err != nil {
		return s, fmt.Errorf("invalid observed revision")
	}
	if s.DirtyStatus, err = run("git", "status", "--porcelain=v1", "--untracked-files=normal"); err != nil {
		return s, err
	}
	s.Dirty = s.DirtyStatus != ""
	s.SnapshotAt = time.Now().UTC()
	for _, name := range []string{"go", "ffmpeg", "ffprobe"} {
		tool := Tool{Name: name}
		s.Tools = append(s.Tools, tool)
		index := len(s.Tools) - 1
		path, err := exec.LookPath(name)
		if err != nil {
			return s, fmt.Errorf("observe %s path: %w", name, err)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return s, err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return s, err
		}
		s.Tools[index].Path = path
		hash, err := hashFile(ctx, path)
		if err != nil {
			return s, fmt.Errorf("observe %s hash: %w", name, err)
		}
		s.Tools[index].SHA256 = hash
		arg := "-version"
		if name == "go" {
			arg = "version"
		}
		version, err := run(path, arg)
		if err != nil {
			return s, err
		}
		if version == "" {
			return s, fmt.Errorf("empty %s version", name)
		}
		s.Tools[index].Version = version
	}
	return s, nil
}
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 1<<30 {
		return "", fmt.Errorf("tool is not a regular file <=1GiB")
	}
	h := sha256.New()
	buf := make([]byte, 131072)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		total += int64(n)
		if total > 1<<30 {
			return "", fmt.Errorf("tool hash size cap")
		}
		_, _ = h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) || total != stat.Size() {
		return "", fmt.Errorf("tool changed while hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteExclusive never replaces an existing file, including a symlink. Failure
// reports use the same path and permissions as successful reports.
func WriteExclusive(path string, r Report) error {
	f, err := reserve(path)
	if err != nil {
		return err
	}
	return writeReport(f, r)
}
func reserve(path string) (*os.File, error) {
	if path == "" || len(path) > 4096 {
		return nil, fmt.Errorf("output must be a new file path")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
func writeReport(f *os.File, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil && len(data) > maxReportBytes {
		err = fmt.Errorf("report exceeds %d bytes", maxReportBytes)
	}
	if err == nil {
		data = append(data, '\n')
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
