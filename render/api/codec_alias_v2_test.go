package renderapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestDecodeV2RejectsExplicitEmptyOptionalBuild(t *testing.T) {
	plan := planFixtureV2(t, "mixed")
	manifest := manifestFixtureV2(t, plan)
	request := requestFixtureV2(plan, manifest)
	for _, kind := range []string{"manifest", "request"} {
		t.Run(kind, func(t *testing.T) {
			var raw []byte
			var err error
			if kind == "manifest" {
				raw, err = CanonicalExecutionManifestV2(manifest)
			} else {
				raw, err = CanonicalRenderRequestV2(request)
			}
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			manifestFields := fields
			if kind == "request" {
				manifestFields = nil
				if err := json.Unmarshal(fields["execution_manifest"], &manifestFields); err != nil {
					t.Fatal(err)
				}
			}
			var media map[string]json.RawMessage
			if err := json.Unmarshal(manifestFields["media_runtime"], &media); err != nil {
				t.Fatal(err)
			}
			media["text_shaper_build"] = json.RawMessage(`""`)
			manifestFields["media_runtime"], err = json.Marshal(media)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "request" {
				fields["execution_manifest"], err = json.Marshal(manifestFields)
				if err != nil {
					t.Fatal(err)
				}
			}
			alias, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			alias, err = canonicaljson.Canonicalize(alias)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(alias, raw) {
				t.Fatal("mutation did not change canonical wire bytes")
			}
			if kind == "manifest" {
				got, err := DecodeExecutionManifestV2(alias)
				if err == nil || !reflect.DeepEqual(got, ExecutionManifestV2{}) {
					t.Fatalf("alias must fail zero: %v", err)
				}
				got, err = DecodeExecutionManifestV2(raw)
				if err != nil {
					t.Fatal(err)
				}
				reencoded, err := CanonicalExecutionManifestV2(got)
				if err != nil || !bytes.Equal(raw, reencoded) {
					t.Fatalf("valid round trip: %v", err)
				}
			} else {
				got, err := DecodeRenderRequestV2(alias)
				if err == nil || !reflect.DeepEqual(got, RenderRequestV2{}) {
					t.Fatalf("alias must fail zero: %v", err)
				}
				got, err = DecodeRenderRequestV2(raw)
				if err != nil {
					t.Fatal(err)
				}
				reencoded, err := CanonicalRenderRequestV2(got)
				if err != nil || !bytes.Equal(raw, reencoded) {
					t.Fatalf("valid round trip: %v", err)
				}
			}
		})
	}
}
