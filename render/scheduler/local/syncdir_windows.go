//go:build windows

package schedulerlocal

import "os"

// Windows does not expose a portable directory-fsync equivalent. OpenJournal
// flushes the opened file handle with File.Sync (FlushFileBuffers); the
// directory step is therefore an explicit no-op on Windows. Every committed
// append is also flushed through the same file handle.
func syncJournalDirectory(_ *os.Root) error {
	return nil
}
