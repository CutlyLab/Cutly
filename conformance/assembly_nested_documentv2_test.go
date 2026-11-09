package conformance_test

import (
	"fmt"
	"testing"
)

func assemblySchemaObjectsV2(value any, path string, visit func(string, map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		// Snapshot child values before visit so a deliberate mutation does not alter
		// traversal or recursively visit the newly inserted unknown member.
		children := make(map[string]any, len(v))
		for k, x := range v {
			children[k] = x
		}
		visit(path, v)
		for k, x := range children {
			assemblySchemaObjectsV2(x, path+"/"+k, visit)
		}
	case []any:
		for i, x := range v {
			assemblySchemaObjectsV2(x, fmt.Sprintf("%s/%d", path, i), visit)
		}
	}
}

func TestDocumentV2AssemblySchemaAllNestedObjects(t *testing.T) {
	schema := assemblySchemaDocumentV2(t)
	raw := fixture(t, "document-v2-chunk-assembly-mixed.valid.json")
	paths := []string{}
	assemblySchemaObjectsV2(decodeSchemaInstance(t, raw), "$", func(path string, _ map[string]any) { paths = append(paths, path) })
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			value := decodeSchemaInstance(t, raw)
			assemblySchemaObjectsV2(value, "$", func(p string, m map[string]any) {
				if p == path {
					m["__unknown_field"] = true
				}
			})
			if err := schema.Validate(value); err == nil {
				t.Fatal("schema accepted unknown nested member")
			}
		})
	}
	for _, path := range paths {
		var keys []string
		assemblySchemaObjectsV2(decodeSchemaInstance(t, raw), "$", func(p string, m map[string]any) {
			if p == path {
				for k := range m {
					keys = append(keys, k)
				}
			}
		})
		for _, key := range keys {
			t.Run(path+"/"+key+"/null", func(t *testing.T) {
				value := decodeSchemaInstance(t, raw)
				assemblySchemaObjectsV2(value, "$", func(p string, m map[string]any) {
					if p == path {
						m[key] = nil
					}
				})
				if err := schema.Validate(value); err == nil {
					t.Fatal("schema accepted null nested member")
				}
			})
		}
	}
}
