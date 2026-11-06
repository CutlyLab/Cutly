//go:build !windows

package schedulerlocal

import (
	"errors"
	"os"
)

// syncJournalDirectory is the POSIX path-entry commit boundary. Syncing the
// file alone does not make a newly created directory entry durable across a
// crash, including when Open is retrying an earlier ambiguous creation.
func syncJournalDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
