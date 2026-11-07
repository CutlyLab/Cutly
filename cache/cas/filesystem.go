package cas

import (
	"io"
	"io/fs"
	"os"
)

// File is the narrow durable-file surface required by Store. It is intentionally injectable so
// fault tests can fail write, fsync, close, seek, and stat boundaries without replacing CAS logic.
type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Stat() (fs.FileInfo, error)
	Sync() error
}

// FileSystem is the full local persistence boundary used by Store. Rename must be atomic within
// one filesystem and must not replace an existing non-empty directory. Store installs a complete
// non-empty scratch wrapper directly at a missing canonical entry path.
type FileSystem interface {
	MkdirAll(path string, permission fs.FileMode) error
	Mkdir(path string, permission fs.FileMode) error
	MkdirTemp(directory, pattern string) (string, error)
	Open(path string) (File, error)
	OpenFile(path string, flags int, permission fs.FileMode) (File, error)
	Lstat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	Rename(oldPath, newPath string) error
	Remove(path string) error
	RemoveAll(path string) error
	Chmod(path string, permission fs.FileMode) error
	SyncDir(path string) error
	AvailableBytes(path string) (uint64, error)
}

// OSFileSystem is the production filesystem implementation.
type OSFileSystem struct{}

func (OSFileSystem) MkdirAll(path string, permission fs.FileMode) error {
	return os.MkdirAll(path, permission)
}

func (OSFileSystem) Mkdir(path string, permission fs.FileMode) error {
	return os.Mkdir(path, permission)
}

func (OSFileSystem) MkdirTemp(directory, pattern string) (string, error) {
	return os.MkdirTemp(directory, pattern)
}

func (OSFileSystem) Open(path string) (File, error) { return os.Open(path) }

func (OSFileSystem) OpenFile(path string, flags int, permission fs.FileMode) (File, error) {
	return os.OpenFile(path, flags, permission)
}

func (OSFileSystem) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

func (OSFileSystem) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

func (OSFileSystem) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

func (OSFileSystem) Remove(path string) error { return os.Remove(path) }

func (OSFileSystem) RemoveAll(path string) error { return os.RemoveAll(path) }

func (OSFileSystem) Chmod(path string, permission fs.FileMode) error {
	return os.Chmod(path, permission)
}

func (OSFileSystem) SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return joinErrors(directory.Sync(), directory.Close())
}

func (OSFileSystem) AvailableBytes(path string) (uint64, error) { return availableBytes(path) }
