package ffprobe

import "github.com/CutlyLab/Cutly/media/probe"

// Options returns the normalized, fingerprint-bound capture policy by value.
// These output limits are not a child-process RSS or input-size sandbox.
func (adapter *Adapter) Options() Options {
	if adapter == nil {
		return Options{}
	}
	return adapter.options
}

// Identity returns the exact identity inserted into successful probe manifests.
// It grants no input-path or runtime authority and does not launch a process.
func (adapter *Adapter) Identity() probe.ProbeIdentity {
	if adapter == nil {
		return probe.ProbeIdentity{}
	}
	return probe.ProbeIdentity{
		Implementation:     Implementation,
		BuildFingerprint:   adapter.buildFingerprint,
		OptionsFingerprint: adapter.optionsFingerprint,
	}
}
