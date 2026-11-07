package architecture

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const cutlyModulePath = "github.com/CutlyLab/Cutly"

var forbiddenImportPrefixes = []string{
	"github.com/ruminate0810/Manify",
	"github.com/LincanXu/Manyfy",
	"github.com/CutlyLab/Manyfy",
}

// TestIndependentModuleBoundary keeps the extraction boundary executable. A nested module can be
// copied to CutlyLab/Cutly only when neither production code nor tests reach back into the Manyfy
// host application.
func TestIndependentModuleBoundary(t *testing.T) {
	root := moduleRoot(t)
	assertModulePath(t, root)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		return checkImports(t, root, path)
	})
	if err != nil {
		t.Fatalf("walk Cutly module: %v", err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func assertModulePath(t *testing.T, root string) {
	t.Helper()
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("open nested Cutly go.mod: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			if fields[1] != cutlyModulePath {
				t.Fatalf("nested module path = %q, want %q", fields[1], cutlyModulePath)
			}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read nested Cutly go.mod: %v", err)
	}
	t.Fatal("nested Cutly go.mod has no module directive")
}

func checkImports(t *testing.T, root, path string) error {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, imported := range parsed.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return fmt.Errorf("decode import in %s: %w", path, err)
		}
		for _, prefix := range forbiddenImportPrefixes {
			if value == prefix || strings.HasPrefix(value, prefix+"/") {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					relative = path
				}
				t.Errorf("%s imports host package %q; Cutly must remain independently buildable", relative, value)
			}
		}
	}
	return nil
}
