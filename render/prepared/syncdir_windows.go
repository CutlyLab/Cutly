//go:build windows

package prepared

// Go has no portable directory fsync on Windows. File Sync and atomic rename
// preserve process-crash consistency; power-loss directory durability remains
// weaker than the POSIX profile and is reported in package documentation.
func syncDirectory(string) error { return nil }
