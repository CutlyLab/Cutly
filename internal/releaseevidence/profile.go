// Package releaseevidence runs a deliberately restricted RC evidence profile.
// It is not general v2 acceptance or a signed runtime-bundle verifier.
package releaseevidence

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

const (
	backend          = "./render/ffmpegbackend"
	daemon           = "./cmd/cutlyd"
	maxReportBytes   = 16 << 20
	maxTotalLogBytes = 2 << 20
)

// Config exposes bounds, never executable names, arbitrary selectors, or shell commands.
type Config struct {
	Profile        string
	Repo           string
	CommandTimeout time.Duration
	Budget         time.Duration
	LogBytes       int
}

func Defaults(profile string) Config {
	budget := 15 * time.Minute
	if profile == "long" {
		budget = 100 * time.Minute
	}
	return Config{Profile: profile, Repo: ".", CommandTimeout: 5 * time.Minute, Budget: budget, LogBytes: 65536}
}

func (c Config) validate() error {
	if c.Profile != "smoke" && c.Profile != "long" {
		return fmt.Errorf("profile must be explicitly smoke or long")
	}
	if c.CommandTimeout < time.Second || c.CommandTimeout > 10*time.Minute {
		return fmt.Errorf("command-timeout must be 1s..10m")
	}
	if c.Budget < time.Second || c.Budget > 105*time.Minute {
		return fmt.Errorf("budget must be 1s..105m")
	}
	if c.Profile == "long" && c.Budget <= time.Hour {
		return fmt.Errorf("long budget must exceed 1h")
	}
	if c.LogBytes < 1024 || c.LogBytes > 262144 {
		return fmt.Errorf("log-bytes must be 1024..262144")
	}
	if c.Repo == "" || len(c.Repo) > 4096 {
		return fmt.Errorf("invalid repository path")
	}
	return nil
}

var faults = []string{
	"TestM1CancellationDuringDecodedEvidenceIsTerminalAndBounded",
	"TestM1RealFailureIsNotReclassifiedByLateCancellation",
	"TestM1ProcessFaultsLeaveNoPartialPublication",
	"TestM1ProbeRejectionMatrix",
	"TestM1ENOSPCAtPublicationRollsBack",
	"TestM1KilledDecoderFailsClosedWithoutPublication",
}
var recovery = []string{
	"TestPublishedRecoveryOpensExactVerifiedMedia",
	"TestPublishedRecoveryReturnsAnonymousImmutableSnapshot",
	"TestPublishedRecoveryRejectsPartialPairs",
	"TestPublishedRecoveryRejectsStaticAndRacingSymlinks",
	"TestPublishedRecoveryRejectsMutatedMediaAndOversizeEvidence",
	"TestPublishedRecoveryBindsJobAndIsConcurrentSafe",
	"TestPublishedRecoveryHonorsCanceledContext",
}

const daemonRecovery = "TestRenderApplicationEndToEndAndOfflinePublicationRecovery"
const repeatability = "TestM1ReferenceProfileTenRunDecodedRepeatability"
const rss = "TestM1DecodedEvidenceRSSDoesNotScaleWithDuration"
const benchmark = "BenchmarkM1DecodedEvidenceStreaming"

// Command describes only an internally constructed invocation. Executor is injectable for tests.
type Command struct {
	Path      string            `json:"path"`
	Args      []string          `json:"args"`
	Dir       string            `json:"-"`
	Env       map[string]string `json:"env,omitempty"`
	Expected  []string          `json:"expected,omitempty"`
	Package   string            `json:"package,omitempty"`
	Benchmark bool              `json:"benchmark,omitempty"`
	Scope     string            `json:"scope"`
}

func testCommand(c Config, path, scope string, names ...string) Command {
	selector := "^("
	for i, n := range names {
		if i > 0 {
			selector += "|"
		}
		selector += regexp.QuoteMeta(n)
	}
	selector += ")$"
	return Command{Path: "go", Dir: c.Repo, Args: []string{"test", "-json", "-count=1", "-parallel=1", "-timeout=" + c.CommandTimeout.String(), "-run", selector, path}, Expected: append([]string(nil), names...), Package: "github.com/CutlyLab/Cutly/" + path[2:], Scope: scope}
}
func benchmarkCommand(c Config) Command {
	cmd := testCommand(c, backend, "synthetic zero-byte decoder streaming; not real-media throughput")
	cmd.Args = []string{"test", "-json", "-count=1", "-timeout=" + c.CommandTimeout.String(), "-run", "^$", "-bench", "^" + benchmark + "$", "-benchtime=1x", "-benchmem", backend}
	cmd.Env = map[string]string{"CUTLY_M1_STREAM_BENCH_MIB": "4096"}
	cmd.Benchmark = true
	return cmd
}
func absoluteConfig(c Config) (Config, error) {
	if err := c.validate(); err != nil {
		return c, err
	}
	path, err := filepath.Abs(c.Repo)
	c.Repo = path
	return c, err
}
