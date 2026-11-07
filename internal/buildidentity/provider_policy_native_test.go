//go:build linux || darwin

package buildidentity

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProviderNativeOpenRejectsSymlinksAndSpecialFiles(t *testing.T) {
	path := providerFixture(t, []byte("never executed"))
	link := path + "-link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(filepath.Dir(path), "alias")
	if err := os.Symlink(filepath.Dir(path), parentLink); err != nil {
		t.Fatal(err)
	}
	fifo := path + "-fifo"
	if err := syscall.Mkfifo(fifo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{link, filepath.Join(parentLink, filepath.Base(path)), fifo, "/dev/null"} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			file, err := openProviderExecutable(name)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatalf("native opener accepted %q", name)
			}
		})
	}
}

func TestProviderAncestorReplacedAfterSnapshotFailsClosed(t *testing.T) {
	path := providerFixture(t, []byte("never executed"))
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	// The native opener independently rejects a link even when an earlier
	// caller snapshot described a real directory. No follow-then-check fallback.
	if err := os.Rename(parent, parent+"-original"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(parent); _ = os.Rename(parent+"-original", parent) })
	if err := os.Symlink(parent+"-original", parent); err != nil {
		t.Fatal(err)
	}
	file, err := openProviderExecutable(path)
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatalf("accepted replaced ancestor previously identified as %v", info.Name())
	}
}
