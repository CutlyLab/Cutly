package localv2

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/transactionv2"
)

// Scan the wire before shape decoding allocates whole arrays/maps. Generic
// node/depth bounds also apply inside RawMessage metadata. Named record arrays
// receive their tighter resource ceiling before allocation of typed records.
// Reject lossy UTF-16 escape replacement before encoding/json normalizes it.
func unpairedSurrogate(raw []byte) bool {
	hex4 := func(b []byte) (uint16, bool) {
		var v uint16
		for _, c := range b {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= uint16(c - '0')
			case c >= 'a' && c <= 'f':
				v |= uint16(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				v |= uint16(c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return v, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
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
				return true
			}
			if v < 0xd800 || v > 0xdbff {
				continue
			}
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return true
			}
			low, ok := hex4(raw[i+3 : i+7])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return true
			}
			i += 6
		}
	}
	return false
}

// Array limits apply only to typed model paths, never coincidental names in
// open metadata. JSON pointer paths through arrays use a literal * component.
func scan(raw []byte, model any) error {
	t := reflect.TypeOf(model)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	caps := map[string]uint64{}
	switch t {
	case reflect.TypeOf(transactionv2.Transaction{}):
		caps["/operations"] = DefaultLimits().MaxOperations
	case reflect.TypeOf(PlanRecord{}):
		caps["/transaction/operations"] = DefaultLimits().MaxOperations
		caps["/inverse/operations"] = DefaultLimits().MaxOperations
	case reflect.TypeOf(CommitRecord{}):
		caps["/inverse/operations"] = DefaultLimits().MaxOperations
	case reflect.TypeOf(diskState{}):
		caps["/plans"] = DefaultLimits().MaxPlans
		caps["/history"] = DefaultLimits().MaxHistory
	}
	if !utf8.Valid(raw) || unpairedSurrogate(raw) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var value func(int, string) error
	value = func(depth int, path string) error {
		nodes++
		if depth > 64 || nodes > 2_000_000 {
			return ErrLimit
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalid
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter == '{' {
			for decoder.More() {
				k, e := decoder.Token()
				if e != nil {
					return ErrInvalid
				}
				s, ok := k.(string)
				if !ok {
					return ErrInvalid
				}
				if err := value(depth+1, path+"/"+s); err != nil {
					return err
				}
			}
			end, e := decoder.Token()
			if e != nil || end != json.Delim('}') {
				return ErrInvalid
			}
			return nil
		}
		if delimiter == '[' {
			maximum := uint64(2_000_000)
			if cap, ok := caps[path]; ok {
				maximum = cap
			}
			var count uint64
			for decoder.More() {
				count++
				if count > maximum {
					return ErrLimit
				}
				if err := value(depth+1, path+"/*"); err != nil {
					return err
				}
			}
			end, e := decoder.Token()
			if e != nil || end != json.Delim(']') {
				return ErrInvalid
			}
			return nil
		}
		return ErrInvalid
	}
	if err := value(0, ""); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
