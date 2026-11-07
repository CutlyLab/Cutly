//go:build darwin

package buildidentity

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProviderDarwinDescriptorReopen(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.OpenFile(path, syscall.O_EVTONLY|syscall.O_NONBLOCK|0x20000000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(fmt.Sprintf("/dev/fd/%d", metadata.Fd()), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	b := make([]byte, 7)
	if _, err := file.Read(b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "fixture" {
		t.Fatalf("read %q", b)
	}
}
