//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package backendstage

import (
	"fmt"
	"os"
	"syscall"
)

func validateReferenceScratchMode(path string, info os.FileInfo, leaf bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot verify Unix owner for reference chunk scratch component %q", ErrConfiguration, path)
	}
	owner := uint32(os.Geteuid())
	permissions := info.Mode().Perm()
	if leaf {
		if stat.Uid != owner || permissions != 0o700 {
			return fmt.Errorf("%w: reference chunk scratch leaf %q must be caller-owned with Unix mode 0700", ErrConfiguration, path)
		}
		return nil
	}
	if permissions&0o022 == 0 {
		return nil
	}
	// Sticky, root/caller-owned shared roots such as /tmp cannot have another
	// user's private leaf renamed. Writable ancestors without that property are
	// substitution-prone and are rejected.
	if info.Mode()&os.ModeSticky == 0 || (stat.Uid != 0 && stat.Uid != owner) {
		return fmt.Errorf("%w: reference chunk scratch ancestor %q has unsafe Unix write permissions", ErrConfiguration, path)
	}
	return nil
}
