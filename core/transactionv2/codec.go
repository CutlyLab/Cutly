package transactionv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var ErrInvalid = errors.New("transactionv2: invalid transaction")
var ErrInternal = errors.New("transactionv2: internal failure")
var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var rawType = reflect.TypeOf(json.RawMessage{})
var idType = reflect.TypeOf(d.ID(""))

func diagnostic(code, path, message string) d.Diagnostic {
	return d.Diagnostic{Code: code, Severity: d.SeverityError, Path: path, Message: message}
}
func invalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalid, err) }

// Decode accepts only the exact canonical representation, including the spelling
// of open JSON numbers. Duplicate/unknown/missing/null fields and lossy Unicode
// are rejected before conversion to the typed model.
func Decode(raw []byte) (Transaction, error) {
	if err := scanJSON(raw, MaxTransactionBytes); err != nil {
		return Transaction{}, invalid(err)
	}
	var tx Transaction
	if err := wirejson.Decode(raw, &tx); err != nil {
		return Transaction{}, invalid(err)
	}
	if ds := Validate(tx); d.HasErrorDiagnostics(ds) {
		return Transaction{}, invalid(errors.New(ds[0].Message))
	}
	canonical, err := videoedit.CanonicalJSON(tx)
	if err != nil {
		return Transaction{}, invalid(err)
	}
	if !bytes.Equal(raw, canonical) {
		return Transaction{}, invalid(errors.New("input must be exact canonical JSON"))
	}
	return tx, nil
}

func CanonicalJSON(tx Transaction) ([]byte, error) {
	if ds := Validate(tx); d.HasErrorDiagnostics(ds) {
		return nil, invalid(errors.New(ds[0].Message))
	}
	return videoedit.CanonicalJSON(tx)
}
func Canonical(tx Transaction) ([]byte, error) { return CanonicalJSON(tx) }

// Digest excludes the digest member itself. If supplied, it must match. The
// number-preserving profile deliberately does not use protocol/canonicaljson.
func Digest(tx Transaction) (string, error) {
	if ds := validate(tx, false); d.HasErrorDiagnostics(ds) {
		return "", invalid(errors.New(ds[0].Message))
	}
	supplied := tx.Digest
	tx.Digest = ""
	hash, err := hashValue(tx)
	if err == nil && supplied != "" && supplied != hash {
		return "", invalid(errors.New("digest mismatch"))
	}
	return hash, err
}
func TransactionDigest(tx Transaction) (string, error) { return Digest(tx) }
func hashValue(v any) (string, error) {
	raw, err := videoedit.CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Create binds a caller-authored proposal to a document and returns an owned,
// canonical proposal. It also safely rebinds an inverse to a newer base; target
// postimage predicates are retained and are still checked by Apply.
func Create(document d.Document, tx Transaction) (Transaction, error) {
	if err := boundedValue(document, MaxDocumentBytes); err != nil {
		return Transaction{}, invalid(err)
	}
	if ds := d.Validate(document); d.HasErrorDiagnostics(ds) {
		return Transaction{}, invalid(errors.New(ds[0].Message))
	}
	hash, err := d.DocumentHash(document)
	if err != nil {
		return Transaction{}, err
	}
	tx.SchemaVersion = SchemaVersion
	tx.Profile = Profile
	tx.DocumentID = document.ID
	tx.ExpectedRevision = document.Revision
	tx.ExpectedDocumentHash = hash
	tx.Digest = ""
	digest, err := Digest(tx)
	if err != nil {
		return Transaction{}, err
	}
	tx.Digest = digest
	raw, err := CanonicalJSON(tx)
	if err != nil {
		return Transaction{}, err
	}
	return Decode(raw)
}

func Validate(tx Transaction) []d.Diagnostic { return validate(tx, true) }
func validate(tx Transaction, checkDigest bool) []d.Diagnostic {
	if err := boundedValue(tx, MaxTransactionBytes); err != nil {
		return []d.Diagnostic{diagnostic(CodeLimit, "/", err.Error())}
	}
	// Detect required nil slices/maps before canonical marshaling can hide null.
	raw, err := json.Marshal(tx)
	if err != nil || len(raw) > MaxTransactionBytes {
		return []d.Diagnostic{diagnostic(CodeLimit, "/", "transaction encoding exceeds profile")}
	}
	var shape Transaction
	if err := wirejson.Decode(raw, &shape); err != nil {
		return []d.Diagnostic{diagnostic(CodeInvalid, "/", err.Error())}
	}
	fail := func(path, msg string) []d.Diagnostic { return []d.Diagnostic{diagnostic(CodeInvalid, path, msg)} }
	if tx.SchemaVersion != SchemaVersion || tx.Profile != Profile {
		return fail("/profile", "unsupported transaction schema/profile")
	}
	if !validID(tx.ID) || !validID(tx.IdempotencyKey) || !validID(string(tx.DocumentID)) {
		return fail("/id", "transaction, idempotency and document IDs must be nonempty bounded unpadded UTF-8")
	}
	if !hashPattern.MatchString(tx.ExpectedDocumentHash) {
		return fail("/expected_document_hash", "canonical document hash required")
	}
	if len(tx.Operations) == 0 || len(tx.Operations) > MaxOperations {
		return fail("/operations", "operation count must be in [1,4096]")
	}
	seen := make(map[string]bool, len(tx.Operations))
	for i, op := range tx.Operations {
		path := fmt.Sprintf("/operations/%d", i)
		if !validID(op.ID) || seen[op.ID] {
			return fail(path+"/id", "operation IDs must be unique and bounded")
		}
		seen[op.ID] = true
		if err := validateOperation(op); err != nil {
			return []d.Diagnostic{diagnostic(CodeOperationInvalid, path, err.Error())}
		}
	}
	if tx.Digest != "" {
		if !hashPattern.MatchString(tx.Digest) {
			return fail("/digest", "invalid digest spelling")
		}
		if checkDigest {
			copy := tx
			copy.Digest = ""
			expected, err := hashValue(copy)
			if err != nil || expected != tx.Digest {
				return fail("/digest", "digest mismatch")
			}
		}
	}
	return nil
}
func validID(s string) bool {
	return len(s) > 0 && len(s) <= MaxIDBytes && strings.TrimSpace(s) == s && utf8.ValidString(s)
}

func validateOperation(op Operation) error {
	payload := ""
	switch op.Kind {
	case AssetAdd, AssetRemove, AssetReplace:
		payload = "Asset"
	case TimelineAdd, TimelineRemove, TimelineReplace:
		payload = "Timeline"
	case TrackAdd, TrackRemove, TrackReplace:
		payload = "Track"
	case ClipAdd, ClipRemove, ClipReplace, ClipDelete:
		payload = "Clip"
	case BusAdd, BusRemove, BusReplace:
		payload = "Bus"
	case ClipMove:
		payload = "Move"
	case ClipTrim:
		payload = "Trim"
	case ClipSplit:
		payload = "Split"
	case ClipRetrack:
		payload = "Retrack"
	case ClipSetDisabled:
		payload = "Disabled"
	case ClipSetAsset:
		payload = "AssetReference"
	case PropertySet:
		payload = "Properties"
	case LaneSet, LaneRemove:
		payload = "Lane"
	case TransitionSet, TransitionRemove:
		payload = "Transition"
	case TrackRoute:
		payload = "Route"
	case MetadataSet, MetadataRemove:
		payload = "Metadata"
	case AnnotationSet, AnnotationRemove:
		payload = "Annotation"
	default:
		return errors.New("unsupported operation")
	}
	v := reflect.ValueOf(op)
	for i := 3; i < v.NumField(); i++ {
		if (v.Type().Field(i).Name == payload) == v.Field(i).IsNil() {
			return errors.New("operation requires exactly its tagged payload")
		}
	}
	if op.Expect != nil && !hashPattern.MatchString(op.Expect.Hash) {
		return errors.New("invalid postimage hash")
	}
	// All identity references (including nested content) are bounded and explicit.
	if op.Kind == TrackRoute && op.Route.BusID == "" {
		if !validID(string(op.Route.TrackID)) {
			return errors.New("track ID required")
		}
	} else if err := walkIDs(v.FieldByName(payload)); err != nil {
		return err
	}
	var valueID, target d.ID
	var pos *int
	var value bool
	mode := ""
	switch op.Kind {
	case AssetAdd, AssetRemove, AssetReplace:
		p := op.Asset
		target = p.AssetID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case TimelineAdd, TimelineRemove, TimelineReplace:
		p := op.Timeline
		target = p.TimelineID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case TrackAdd, TrackRemove, TrackReplace:
		p := op.Track
		target = p.TrackID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case ClipAdd, ClipRemove, ClipReplace, ClipDelete:
		p := op.Clip
		target = p.ClipID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case BusAdd, BusRemove, BusReplace:
		p := op.Bus
		target = p.BusID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case LaneSet, LaneRemove:
		p := op.Lane
		target = p.LaneID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case TransitionSet, TransitionRemove:
		p := op.Transition
		target = p.TransitionID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	case AnnotationSet, AnnotationRemove:
		p := op.Annotation
		target = p.AnnotationID
		pos = p.Position
		value = p.Value != nil
		if value {
			valueID = p.Value.ID
		}
	}
	if target != "" {
		if strings.HasSuffix(string(op.Kind), "remove") || op.Kind == ClipDelete {
			mode = "remove"
		} else {
			mode = "write"
		}
		if mode == "remove" {
			if value || pos != nil {
				return errors.New("remove forbids value and position")
			}
		} else if !value || pos == nil || *pos < 0 || valueID != target {
			return errors.New("write requires matching typed value and explicit nonnegative position")
		}
	}
	if op.Metadata != nil {
		if op.Metadata.Key == "" {
			return errors.New("metadata key required")
		}
		if op.Kind == MetadataSet && len(op.Metadata.Value) == 0 {
			return errors.New("metadata_set value required")
		}
		if op.Kind == MetadataRemove && len(op.Metadata.Value) != 0 {
			return errors.New("metadata_remove forbids value")
		}
	}
	if op.Retrack != nil && op.Retrack.Position < 0 {
		return errors.New("retrack position must be nonnegative")
	}
	if op.Split != nil && (op.Split.Left.ID != op.Split.ClipID || op.Split.Right.ID == op.Split.ClipID) {
		return errors.New("split must retain left ID and supply a distinct right ID")
	}
	return nil
}
func walkIDs(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return walkIDs(v.Elem())
	}
	if v.Type() == idType {
		if !validID(v.String()) {
			return errors.New("identity references must be bounded nonempty unpadded UTF-8")
		}
		return nil
	}
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if strings.Contains(f.Tag.Get("json"), "omitempty") && v.Field(i).IsZero() {
				continue
			}
			if err := walkIDs(v.Field(i)); err != nil {
				return err
			}
		}
	}
	if v.Kind() == reflect.Slice && v.Type() != rawType {
		for i := 0; i < v.Len(); i++ {
			if err := walkIDs(v.Index(i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// boundedValue runs before any marshal. The conservative encoded-size budget
// includes JSON escaping, structural nodes and raw JSON. Cycles terminate at the
// depth/node cap. No collection of attacker-sized map keys is allocated here.
func boundedValue(value any, limit int) error {
	nodes, size := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		nodes++
		if depth > MaxDepth || nodes > MaxNodes {
			return errors.New("depth/node limit exceeded")
		}
		if !v.IsValid() {
			return nil
		}
		if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem(), depth+1)
		}
		if v.Type() == rawType {
			raw := v.Bytes()
			if len(raw) == 0 {
				return nil
			}
			size += len(raw)
			// encoding/json HTML-escapes RawMessage strings as well. Account
			// for expansion before it can allocate an oversized marshal buffer.
			for _, c := range raw {
				if c == '<' || c == '>' || c == '&' {
					size += 5
				}
			}
			size += 3 * (bytes.Count(raw, []byte(" ")) + bytes.Count(raw, []byte(" ")))
			if size > limit {
				return errors.New("encoded byte limit exceeded")
			}
			count, err := scanJSONBudget(raw, limit, MaxDepth-depth, MaxNodes-nodes)
			nodes += count
			return err
		}
		size += 2
		switch v.Kind() {
		case reflect.Int:
			if v.Int() < -2147483648 || v.Int() > 2147483647 {
				return errors.New("portable int32 range exceeded")
			}
			size += 12
		case reflect.String:
			s := v.String()
			if !utf8.ValidString(s) || len(s) > MaxStringBytes {
				return errors.New("invalid UTF-8 or string limit exceeded")
			}
			if v.Type() == idType && len(s) > MaxIDBytes {
				return errors.New("ID byte limit exceeded")
			}
			size += len(s)
			for _, r := range s {
				if r == ' ' || r == ' ' {
					size += 3
				} else if r < ' ' || r == '<' || r == '>' || r == '&' {
					size += 5
				} else if r == '"' || r == '\\' {
					size++
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if !f.IsExported() {
					continue
				}
				size += len(f.Name) + 4
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Map:
			if v.Len() > MaxNodes-nodes {
				return errors.New("node limit exceeded")
			}
			it := v.MapRange()
			for it.Next() {
				if err := walk(it.Key(), depth+1); err != nil {
					return err
				}
				if err := walk(it.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if v.Len() > MaxNodes-nodes {
				return errors.New("node limit exceeded")
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		default:
			size += 20
		}
		if size > limit {
			return errors.New("encoded byte limit exceeded")
		}
		return nil
	}
	return walk(reflect.ValueOf(value), 0)
}

func scanJSON(raw []byte, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return errors.New("invalid JSON encoding or byte limit")
	}
	// Scan string tokens before decoding to bound allocations and reject surrogate
	// replacement. Syntax remains encoding/json/wirejson's responsibility.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		start := i
		i++
		for ; i < len(raw) && raw[i] != '"'; i++ {
			if i-start > 6*MaxStringBytes {
				return errors.New("string limit exceeded")
			}
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				break
			}
			if raw[i] != 'u' || i+4 >= len(raw) {
				continue
			}
			u, ok := hex4(raw[i+1 : i+5])
			if !ok {
				continue
			}
			i += 4
			if u >= 0xdc00 && u <= 0xdfff {
				return errors.New("unpaired surrogate")
			}
			if u >= 0xd800 && u <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return errors.New("unpaired surrogate")
				}
				low, ok := hex4(raw[i+3 : i+7])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired surrogate")
				}
				i += 6
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	depth, nodes := 0, 0
	for {
		t, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		nodes++
		if nodes > MaxNodes {
			return errors.New("node limit exceeded")
		}
		switch x := t.(type) {
		case json.Delim:
			if x == '{' || x == '[' {
				depth++
				if depth > MaxDepth {
					return errors.New("depth limit exceeded")
				}
			} else {
				depth--
			}
		case string:
			if len(x) > MaxStringBytes {
				return errors.New("string limit exceeded")
			}
		}
	}
	return wirejson.Validate(raw)
}
func scanJSONBudget(raw []byte, limit, maxDepth, maxNodes int) (int, error) {
	if err := scanJSON(raw, limit); err != nil {
		return 0, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	nodes, depth := 0, 0
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nodes, err
		}
		nodes++
		if nodes > maxNodes {
			return nodes, errors.New("aggregate node limit exceeded")
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
				if depth > maxDepth {
					return nodes, errors.New("aggregate depth limit exceeded")
				}
			} else {
				depth--
			}
		}
	}
	return nodes, nil
}
func hex4(b []byte) (uint16, bool) {
	var n uint16
	for _, c := range b {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n += uint16(c - '0')
		case c >= 'a' && c <= 'f':
			n += uint16(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n += uint16(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return n, true
}
