package ffmpegbackend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateInputLocatorUsesConfiguredRootPolicy(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	if err := os.WriteFile(input, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := resolveInputRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, authorized := range roots {
			_ = authorized.handle.Close()
		}
	})
	backend := &Backend{inputRoots: roots}
	if err := backend.ValidateInputLocator(input); err != nil {
		t.Fatalf("authorized locator rejected: %v", err)
	}
	if err := backend.ValidateInputLocator(filepath.Join(filepath.Dir(root), "outside.mp4")); err == nil {
		t.Fatal("locator outside configured roots was accepted")
	}
	unclean := root + string(os.PathSeparator) + "." + string(os.PathSeparator) + "input.mp4"
	if err := backend.ValidateInputLocator(unclean); err == nil {
		t.Fatal("unclean locator was accepted")
	}
}
