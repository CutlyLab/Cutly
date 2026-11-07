//go:build !windows

package local

import "os"

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errorsJoin(directory.Sync(), directory.Close())
}
