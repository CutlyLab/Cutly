//go:build windows

package local

// Go has no portable directory fsync on Windows. Atomic replace and file Sync
// still protect process-crash consistency, but the directory entry has a weaker
// power-loss guarantee than the POSIX implementation.
func syncDirectory(string) error { return nil }
