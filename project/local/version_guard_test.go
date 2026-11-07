package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestV1RejectsReservedV2Root(t *testing.T) {
	if !lockingSupported() {
		t.Skip("project locking unsupported")
	}
	for _, kind := range []string{"directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, ".cutly"), 0700); err != nil {
				t.Fatal(err)
			}
			reservation := filepath.Join(dir, ".cutly", "v2")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(reservation, 0700)
			case "file":
				err = os.WriteFile(reservation, []byte("reserved"), 0600)
			case "symlink":
				err = os.Symlink("missing-target", reservation)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Create(context.Background(), dir, fixtureDocument(), Options{}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create: %v", err)
			}
			service, err := New(dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Inspect(context.Background()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Inspect: %v", err)
			}
			for _, name := range []string{DocumentName, stateName, intentName, markerName} {
				if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("v1 wrote %s: %v", name, err)
				}
			}
		})
	}
}
