package wirejson

import (
	"encoding/json"
	"strings"
	"testing"
)

type strictFixture struct {
	SchemaVersion int                        `json:"schema_version"`
	Enabled       bool                       `json:"enabled"`
	Nested        strictNested               `json:"nested"`
	Extensions    map[string]json.RawMessage `json:"extensions,omitempty"`
}

type strictNested struct {
	Count int `json:"count"`
}

type nullableFixture struct {
	Value *strictNested `json:"value" wirejson:"nullable"`
	Other *strictNested `json:"other"`
}

func TestDecodeRejectsAmbiguousStructWireValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "duplicate root", raw: `{"schema_version":9,"schema_version":1,"enabled":false,"nested":{"count":0}}`, want: `duplicate object member "schema_version"`},
		{name: "duplicate nested", raw: `{"schema_version":1,"enabled":false,"nested":{"count":9,"count":0}}`, want: `duplicate object member "count"`},
		{name: "duplicate raw message", raw: `{"schema_version":1,"enabled":false,"nested":{"count":0},"extensions":{"x":{"mode":"safe","mode":"evil"}}}`, want: `duplicate object member "mode"`},
		{name: "case alias", raw: `{"SCHEMA_VERSION":1,"enabled":false,"nested":{"count":0}}`, want: `unknown object member "SCHEMA_VERSION"`},
		{name: "missing zero integer", raw: `{"enabled":false,"nested":{"count":0}}`, want: `required object member "schema_version" is missing`},
		{name: "missing zero boolean", raw: `{"schema_version":1,"nested":{"count":0}}`, want: `required object member "enabled" is missing`},
		{name: "missing nested zero", raw: `{"schema_version":1,"enabled":false,"nested":{}}`, want: `required object member "count" is missing`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value strictFixture
			err := Decode([]byte(test.raw), &value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDecodeAllowsOpenRawMessageKeys(t *testing.T) {
	raw := []byte(`{"schema_version":1,"enabled":false,"nested":{"count":0},"extensions":{"Mixed-Case":{"arbitrary":null}}}`)
	var value strictFixture
	if err := Decode(raw, &value); err != nil {
		t.Fatal(err)
	}
	if string(value.Extensions["Mixed-Case"]) != `{"arbitrary":null}` {
		t.Fatalf("extensions = %s", value.Extensions["Mixed-Case"])
	}
}

func TestDecodeAllowsOnlyExplicitlyTaggedRequiredNull(t *testing.T) {
	var value nullableFixture
	if err := Decode([]byte(`{"value":null,"other":{"count":1}}`), &value); err != nil || value.Value != nil || value.Other == nil {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if err := Decode([]byte(`{"other":{"count":1}}`), &value); err == nil || !strings.Contains(err.Error(), `required object member "value" is missing`) {
		t.Fatalf("missing nullable field error=%v", err)
	}
	if err := Decode([]byte(`{"value":{"count":1},"other":null}`), &value); err == nil || !strings.Contains(err.Error(), "null is not valid") {
		t.Fatalf("unmarked null error=%v", err)
	}
}
