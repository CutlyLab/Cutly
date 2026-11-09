package prepared

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// Token scan before wirejson's allocating shape pass. Applies inside open
// metadata as well as typed records. No whole arrays/maps are allocated here.
func scanEnvironmentV2(ctx context.Context, raw []byte, maximum, maxNodes, maxDepth int) error {
	if err := contextEnvironmentV2(ctx); err != nil {
		return err
	}
	if len(raw) == 0 {
		return ErrInvalidEnvironmentV2
	}
	if len(raw) > maximum {
		return ErrLimit
	}
	if !utf8.Valid(raw) {
		return ErrInvalidEnvironmentV2
	}
	if err := unicodeEnvironmentV2(ctx, raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	partStart, partNodes, partMaximum := 0, 0, 0
	var root reflect.Type
	if maximum == MaxDocumentBytesV2 {
		root = reflect.TypeOf(documentv2.Document{})
	}
	if maximum == MaxRecordBytesV2 {
		root = reflect.TypeOf(recordV2{})
	}
	if maximum == MaxPlanBytesV2 {
		root = reflect.TypeOf(renderplanv2.Plan{})
	}
	if maximum == MaxManifestBytesV2 {
		root = reflect.TypeOf(renderapi.ExecutionManifestV2{})
	}
	var value func(int, reflect.Type) error
	value = func(depth int, expected reflect.Type) error {
		for expected != nil && expected.Kind() == reflect.Pointer {
			expected = expected.Elem()
		}
		if expected == reflect.TypeOf(json.RawMessage{}) {
			// Inspect the complete encoded value before Token can allocate any
			// of it, and before wirejson builds/copies its generic/typed shapes.
			if err := rawSpanEnvironmentV2(ctx, raw, int(d.InputOffset()), renderplanv2.MaxStringBytes); err != nil {
				return err
			}
			expected = nil // open JSON inside this already-bounded raw subtree
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > maxNodes || depth > maxDepth {
			return ErrLimit
		}
		if partMaximum != 0 && (nodes-partNodes > renderplanv2.MaxNodes || depth-1 > renderplanv2.MaxDepth || int(d.InputOffset())-partStart > partMaximum+1) {
			return ErrLimit
		}
		t, err := d.Token()
		if err != nil {
			return ErrInvalidEnvironmentV2
		}
		if s, ok := t.(string); ok && len(s) > renderplanv2.MaxStringBytes {
			return ErrLimit
		}
		if expected != nil {
			// Preserve type propagation: an array masquerading as a struct/map
			// must not turn its typed children into unbounded open JSON.
			switch expected.Kind() {
			case reflect.Struct, reflect.Map:
				if t != json.Delim('{') {
					return ErrInvalidEnvironmentV2
				}
			case reflect.Slice, reflect.Array:
				if t != json.Delim('[') {
					return ErrInvalidEnvironmentV2
				}
			default:
				if t == nil {
					return ErrInvalidEnvironmentV2
				}
				if _, composite := t.(json.Delim); composite {
					return ErrInvalidEnvironmentV2
				}
			}
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return ErrInvalidEnvironmentV2
		}
		count := 0
		for d.More() {
			var child reflect.Type
			if expected != nil {
				switch expected.Kind() {
				case reflect.Slice, reflect.Array, reflect.Map:
					child = expected.Elem()
				}
			}
			count++
			if count > renderplanv2.MaxCollection {
				return ErrLimit
			}
			if delim == '{' {
				key, e := d.Token()
				if e != nil {
					return ErrInvalidEnvironmentV2
				}
				s, ok := key.(string)
				if !ok {
					return ErrInvalidEnvironmentV2
				}
				if len(s) > renderplanv2.MaxStringBytes {
					return ErrLimit
				}
				if expected != nil && expected.Kind() == reflect.Struct {
					found := false
					for i := 0; i < expected.NumField(); i++ {
						field := expected.Field(i)
						if field.IsExported() && strings.Split(field.Tag.Get("json"), ",")[0] == s {
							child, found = field.Type, true
							break
						}
					}
					// Reject unknown/case-aliased members before their values can
					// cause any proportional token or shape allocation.
					if !found {
						return ErrInvalidEnvironmentV2
					}
				}
				// Enforce each embedded byte/node/depth ceiling during the outer
				// scan, before wirejson allocates even its RawMessage fields.
				if maximum == MaxRecordBytesV2 && depth == 0 {
					switch s {
					case "document":
						partMaximum = MaxDocumentBytesV2
						child = reflect.TypeOf(documentv2.Document{})
					case "plan":
						partMaximum = MaxPlanBytesV2
						child = reflect.TypeOf(renderplanv2.Plan{})
					case "execution_manifest":
						partMaximum = MaxManifestBytesV2
						child = reflect.TypeOf(renderapi.ExecutionManifestV2{})
					}
					partStart, partNodes = int(d.InputOffset()), nodes
				}
			}
			if expected == reflect.TypeOf(renderapi.ProgramSelectionV2{}) && child == reflect.TypeOf(json.RawMessage{}) {
				if err := rawSpanEnvironmentV2(ctx, raw, int(d.InputOffset()), renderapi.MaxConfigurationBytesV2); err != nil {
					return err
				}
			}
			if err := value(depth+1, child); err != nil {
				return err
			}
			if depth == 0 && partMaximum != 0 {
				if int(d.InputOffset())-partStart > partMaximum+1 {
					return ErrLimit
				}
				partMaximum = 0
			}
		}
		end, e := d.Token()
		if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return ErrInvalidEnvironmentV2
		}
		return nil
	}
	if err := value(0, root); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalidEnvironmentV2
	}
	return ctx.Err()
}

// rawSpanEnvironmentV2 bounds an opaque JSON leaf without creating its strings,
// slices or maps. Syntax is checked by the token scan; this lexical pass only
// locates the complete value and counts its exact raw bytes. The preceding
// Decoder offset may point at a colon/comma; whitespace is not part of the leaf.
func rawSpanEnvironmentV2(ctx context.Context, raw []byte, offset, maximum int) error {
	for offset < len(raw) && (raw[offset] == ':' || raw[offset] == ',' || raw[offset] == ' ' || raw[offset] == '\n' || raw[offset] == '\r' || raw[offset] == '\t') {
		offset++
	}
	if offset == len(raw) {
		return ErrInvalidEnvironmentV2
	}
	start, depth, inString, escaped := offset, 0, false, false
	composite := raw[start] == '{' || raw[start] == '['
	quoted := raw[start] == '"'
	for i := start; i < len(raw); i++ {
		if i-start >= maximum {
			// Primitive terminators are outside the raw value; allow exactly
			// maximum bytes before one, just as for strings/composites.
			if !composite && !quoted && (raw[i] == ',' || raw[i] == '}' || raw[i] == ']' || raw[i] == ' ' || raw[i] == '\n' || raw[i] == '\r' || raw[i] == '\t') {
				return nil
			}
			return ErrLimit
		}
		if (i-start)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		c := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
				if quoted && depth == 0 {
					return nil
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if !composite {
				return nil
			}
			depth--
			if depth == 0 {
				return nil
			}
		case ',', ' ', '\n', '\r', '\t':
			if !composite {
				return nil
			}
		}
	}
	if !composite && !inString {
		return nil
	}
	return ErrInvalidEnvironmentV2
}

// Check raw escapes before encoding/json can replace a lone UTF-16 surrogate.
// Also bounds each encoded string token before Decoder.Token allocates it (a
// decoded byte can use at most six encoded bytes under this profile).
func unicodeEnvironmentV2(ctx context.Context, raw []byte) error {
	hex4 := func(b []byte) (uint16, bool) {
		var n uint16
		for _, c := range b {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n |= uint16(c - '0')
			case c >= 'a' && c <= 'f':
				n |= uint16(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				n |= uint16(c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return n, true
	}
	for i := 0; i < len(raw); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if raw[i] != '"' {
			// No valid typed numeric field needs a long token; open Document
			// numbers live in RawMessage, already limited by S1 to 1 MiB.
			// Bound the token before Decoder.Token creates its json.Number.
			if raw[i] == '-' || (raw[i] >= '0' && raw[i] <= '9') {
				start := i
				for i < len(raw) && ((raw[i] >= '0' && raw[i] <= '9') || raw[i] == '-' || raw[i] == '+' || raw[i] == '.' || raw[i] == 'e' || raw[i] == 'E') {
					if i-start >= renderplanv2.MaxStringBytes {
						return ErrLimit
					}
					if i%4096 == 0 {
						if err := ctx.Err(); err != nil {
							return err
						}
					}
					i++
				}
				i--
			}
			continue
		}
		start := i
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if i-start > renderplanv2.MaxStringBytes*6 {
				return ErrLimit
			}
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) || raw[i] != 'u' || i+4 >= len(raw) {
				continue
			}
			v, ok := hex4(raw[i+1 : i+5])
			if !ok {
				continue
			}
			i += 4
			if v >= 0xdc00 && v <= 0xdfff {
				return ErrInvalidEnvironmentV2
			}
			if v < 0xd800 || v > 0xdbff {
				continue
			}
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return ErrInvalidEnvironmentV2
			}
			low, ok := hex4(raw[i+3 : i+7])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return ErrInvalidEnvironmentV2
			}
			i += 6
		}
	}
	return ctx.Err()
}
