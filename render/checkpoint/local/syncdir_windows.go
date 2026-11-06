//go:build windows

package local

// Go exposes no portable directory fsync on Windows. Checkpoint files are
// synced and directory rename is atomic, but the power-loss directory-entry
// guarantee is weaker than the Darwin/Linux profile.
func syncDirectory(string) error { return nil }
