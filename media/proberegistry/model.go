package proberegistry

const (
	DefaultMaxEntries           = 1 << 16
	DefaultMaxScratchEntries    = 1024
	DefaultMaxQuarantineEntries = 4096
	DefaultMaxRecordBytes       = 4 << 20
	MinRecordBytes              = 4 << 10

	MaxEntries           = 1 << 20
	MaxScratchEntries    = 1 << 16
	MaxQuarantineEntries = 1 << 16
	MaxRecordBytes       = 16 << 20
	MaxStreams           = 4096
	MaxCanonicalValues   = 1 << 18
)

// Options bounds all persistent and transient registry namespaces. Zero
// values select the documented defaults.
type Options struct {
	MaxEntries           int
	MaxScratchEntries    int
	MaxQuarantineEntries int
	MaxRecordBytes       int
}
