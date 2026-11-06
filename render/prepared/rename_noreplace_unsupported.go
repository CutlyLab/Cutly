//go:build !darwin && !linux && !windows

package prepared

func publicationSupported() bool { return false }

func renameNoReplace(string, string) error { return ErrUnsupported }
