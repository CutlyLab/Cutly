package sfntglyf

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

func TestOutlineSetTextContractCompatibilityWithoutProductionDependency(t *testing.T) {
	if OutlineSetOutlineProfileV1 != documentv2.TextOutlineProfileQ26_6V1 {
		t.Fatalf("outline profile=%q document=%q", OutlineSetOutlineProfileV1, documentv2.TextOutlineProfileQ26_6V1)
	}
	tests := []struct {
		op    OutlineCommandOp
		text  textlayout.OutlineCommandOp
		tag   uint8
		arity uint8
	}{
		{OutlineMoveTo, textlayout.MoveTo, 1, 1},
		{OutlineLineTo, textlayout.LineTo, 2, 1},
		{OutlineQuadTo, textlayout.QuadTo, 3, 2},
		{OutlineClose, textlayout.Close, 4, 0},
	}
	for _, test := range tests {
		if test.op.String() != string(test.text) || uint8(test.op) != test.tag {
			t.Fatalf("op=%v string=%q text=%q tag=%d", test.op, test.op.String(), test.text, test.tag)
		}
		command := outlineSetCommand{op: test.op, pointCount: test.arity}
		if command.pointCount != test.arity {
			t.Fatal("arity drift")
		}
	}
	encoding := textlayout.Descriptor().CanonicalEncoding
	if !strings.Contains(encoding, "move_to=1,line_to=2,quad_to=3,close=4") {
		t.Fatalf("textlayout canonical tags drift: %s", encoding)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "github.com/CutlyLab/Cutly/core/documentv2" || strings.HasPrefix(path, "github.com/CutlyLab/Cutly/render/") {
				t.Fatalf("production dependency boundary violated: %s imports %s", name, path)
			}
		}
	}
}
