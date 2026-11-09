package videodecode

// Options returns the normalized, fingerprint-bound resource policy by value.
// It describes logical Go storage and decode limits, not a child-process RSS cap.
func (adapter *Adapter) Options() Options {
	if adapter == nil {
		return Options{}
	}
	return adapter.options
}

// BuildIdentity returns the resolver-supplied identity bound during construction.
// Returning it does not independently verify the runtime dependency closure.
func (adapter *Adapter) BuildIdentity() BuildIdentity {
	if adapter == nil {
		return BuildIdentity{}
	}
	return adapter.identity
}
