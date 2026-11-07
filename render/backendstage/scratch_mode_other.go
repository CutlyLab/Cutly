//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package backendstage

import "os"

// Non-Unix platforms do not expose an ownership/DACL proof through
// os.FileMode. The common validator still rejects every symlinked/non-directory
// component; host configuration remains responsible for a private DACL.
func validateReferenceScratchMode(_ string, _ os.FileInfo, _ bool) error { return nil }
