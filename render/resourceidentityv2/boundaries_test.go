package resourceidentityv2

import (
	"context"
	"errors"
	"testing"
)

func TestCodecAndComparisonBoundariesFailZero(t *testing.T) {
	actual, r := pngInspection(t)
	v, e := Verify(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := v.CanonicalJSON()
	if e != nil {
		t.Fatal(e)
	}
	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{"verify", func(c context.Context) error {
			v, e := Verify(c, r)
			if e != nil && v.receipt != nil {
				t.Error("partial verified")
			}
			return e
		}},
		{"decode", func(c context.Context) error {
			v, e := Decode(c, raw)
			if e != nil && v.receipt != nil {
				t.Error("partial decoded")
			}
			return e
		}},
		{"fingerprint", func(c context.Context) error {
			fp, e := Fingerprint(c, r)
			if e != nil && fp != "" {
				t.Error("partial fingerprint")
			}
			return e
		}},
		{"equal", func(c context.Context) error {
			equal, e := Equal(c, r, r)
			if e != nil && equal {
				t.Error("partial equality")
			}
			return e
		}},
		{"matches", func(c context.Context) error { return Matches(c, r.Resource, r, actual) }},
		{"check", func(c context.Context) error { return Check(c, r.Resource, staticClaim(r), actual) }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			full := &stepContext{Context: context.Background()}
			if e := op.run(full); e != nil {
				t.Fatal(e)
			}
			for _, n := range []int{1, full.calls/2 + 1, full.calls} {
				if e := op.run(&stepContext{Context: context.Background(), stop: n}); !errors.Is(e, context.Canceled) {
					t.Fatalf("cancel %d/%d: %v", n, full.calls, e)
				}
				if e := op.run(&stepContext{Context: context.Background(), panicAt: n}); !errors.Is(e, ErrPanic) {
					t.Fatalf("panic %d/%d: %v", n, full.calls, e)
				}
			}
		})
	}
}
