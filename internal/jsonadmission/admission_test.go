package jsonadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckBoundsBeforeEncoding(t *testing.T) {
	type item struct {
		Name string          `json:"name"`
		Raw  json.RawMessage `json:"raw,omitempty"`
	}
	value := []item{{Name: "ok", Raw: json.RawMessage(`{"a":1}`)}}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), value, uint64(len(encoded))*8, 32); err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), []item{{Name: strings.Repeat("x", 1024)}}, 64, 32); !errors.Is(err, ErrLimit) {
		t.Fatalf("byte limit error = %v", err)
	}
	if err := Check(context.Background(), make([]item, 32), 1<<20, 8); !errors.Is(err, ErrLimit) {
		t.Fatalf("value limit error = %v", err)
	}
}

func TestCheckCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Check(ctx, []string{"value"}, 1024, 32); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestCheckRawMessageExpansionAndNestedValues(t *testing.T) {
	type envelope struct {
		Raw json.RawMessage `json:"raw"`
	}
	if err := Check(context.Background(), envelope{Raw: json.RawMessage(`"<<<<<<<<<<<<<<<<"`)}, 64, 64); !errors.Is(err, ErrLimit) {
		t.Fatalf("raw expansion error = %v", err)
	}
	nested := append([]byte{'['}, bytes.Repeat([]byte("0,"), 32)...)
	nested[len(nested)-1] = ']'
	if err := Check(context.Background(), envelope{Raw: nested}, 1<<20, 16); !errors.Is(err, ErrLimit) {
		t.Fatalf("raw value-count error = %v", err)
	}
	if err := Check(context.Background(), envelope{Raw: json.RawMessage(`{"a":1,"a":2}`)}, 1<<20, 64); err == nil {
		t.Fatal("duplicate raw object member was accepted")
	}
	deep := append(bytes.Repeat([]byte{'['}, 257), '0')
	deep = append(deep, bytes.Repeat([]byte{']'}, 257)...)
	if err := Check(context.Background(), envelope{Raw: deep}, 1<<20, 1024); !errors.Is(err, ErrLimit) {
		t.Fatalf("raw depth error = %v", err)
	}
	step := &cancelOnSecondCheck{Context: context.Background()}
	if err := Check(step, envelope{Raw: json.RawMessage(`"value"`)}, 1<<20, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("raw cancellation error = %v", err)
	}
}

type cancelOnSecondCheck struct {
	context.Context
	checks int
}

func (ctx *cancelOnSecondCheck) Err() error {
	ctx.checks++
	if ctx.checks >= 2 {
		return context.Canceled
	}
	return nil
}
