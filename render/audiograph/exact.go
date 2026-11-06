// Package audiograph implements Cutly's deterministic, codec-independent reference audio mix
// graph. It evaluates exact nominal-full-scale samples and deliberately stops before PCM
// quantization, dithering, encoding, or container delay handling.
package audiograph

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
)

// Exact is an immutable arbitrary-precision rational sample value. Zero-value Exact is invalid;
// use Zero, One, NewInt64, NewRatio, or NewExact. The type has no mutating accessors and all big
// integer inputs and outputs are defensive copies.
//
// JSON uses decimal strings so evidence fixtures do not lose precision:
// {"numerator":"1","denominator":"2"}.
type Exact struct {
	numerator   big.Int
	denominator big.Int
}

// NewExact constructs a reduced rational from defensive copies of numerator and denominator.
func NewExact(numerator, denominator *big.Int) (Exact, error) {
	if numerator == nil || denominator == nil || denominator.Sign() == 0 {
		return Exact{}, runtimeError(CodeInputInvalid, "exact", ErrInputInvalid)
	}
	value := new(big.Rat).SetFrac(new(big.Int).Set(numerator), new(big.Int).Set(denominator))
	return exactFromBig(value), nil
}

// NewRatio constructs a reduced exact rational from signed 64-bit terms.
func NewRatio(numerator, denominator int64) (Exact, error) {
	return NewExact(big.NewInt(numerator), big.NewInt(denominator))
}

// NewInt64 constructs an exact integer.
func NewInt64(value int64) Exact { return exactFromBig(new(big.Rat).SetInt64(value)) }

// ParseExact parses the canonical human form "numerator/denominator". Integers may omit `/1`.
func ParseExact(value string) (Exact, error) {
	parsed, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok {
		return Exact{}, runtimeError(CodeInputInvalid, "exact", ErrInputInvalid)
	}
	return exactFromBig(parsed), nil
}

// Zero returns exact silence.
func Zero() Exact { return NewInt64(0) }

// One returns exact unity gain.
func One() Exact { return NewInt64(1) }

func exactFromBig(value *big.Rat) Exact {
	var result Exact
	if value == nil {
		return result
	}
	result.numerator.Set(value.Num())
	result.denominator.Set(value.Denom())
	return result
}

func exactFromDocument(value documentv2.Rational) (Exact, error) {
	if err := value.Validate(); err != nil {
		return Exact{}, runtimeError(CodeInputInvalid, "rational", ErrInputInvalid)
	}
	return exactFromBig(new(big.Rat).SetFrac64(value.Numerator, value.Denominator)), nil
}

func exactFromEvaluator(value evaluator.ExactRational) (Exact, error) {
	return NewExact(value.Numerator(), value.Denominator())
}

func (value Exact) valid() bool { return value.denominator.Sign() > 0 }

func (value Exact) rat() (*big.Rat, error) {
	if !value.valid() {
		return nil, runtimeError(CodeSourceSampleInvalid, "sample", ErrSourceSampleInvalid)
	}
	return new(big.Rat).SetFrac(
		new(big.Int).Set(&value.numerator),
		new(big.Int).Set(&value.denominator),
	), nil
}

// Valid reports whether the value was built by an Exact constructor.
func (value Exact) Valid() bool { return value.valid() }

// Numerator returns a defensive copy.
func (value Exact) Numerator() *big.Int { return new(big.Int).Set(&value.numerator) }

// Denominator returns a defensive copy.
func (value Exact) Denominator() *big.Int { return new(big.Int).Set(&value.denominator) }

// String returns the canonical reduced numerator/denominator form.
func (value Exact) String() string {
	if !value.valid() {
		return "invalid"
	}
	return value.numerator.String() + "/" + value.denominator.String()
}

// Equal compares two valid exact values.
func (value Exact) Equal(other Exact) bool {
	return value.valid() && other.valid() &&
		value.numerator.Cmp(&other.numerator) == 0 && value.denominator.Cmp(&other.denominator) == 0
}

// Sign returns -1, 0, or 1. Invalid values return 0; hostile runtime inputs are rejected before
// arithmetic, so Sign is intended for already validated results.
func (value Exact) Sign() int {
	if !value.valid() {
		return 0
	}
	return value.numerator.Sign()
}

func (value Exact) MarshalJSON() ([]byte, error) {
	if !value.valid() {
		return nil, fmt.Errorf("audiograph: cannot marshal invalid exact value")
	}
	return json.Marshal(struct {
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
	}{Numerator: value.numerator.String(), Denominator: value.denominator.String()})
}

func exactAdd(left, right Exact) (Exact, error) {
	l, err := left.rat()
	if err != nil {
		return Exact{}, err
	}
	r, err := right.rat()
	if err != nil {
		return Exact{}, err
	}
	return exactFromBig(new(big.Rat).Add(l, r)), nil
}

func exactSub(left, right Exact) (Exact, error) {
	l, err := left.rat()
	if err != nil {
		return Exact{}, err
	}
	r, err := right.rat()
	if err != nil {
		return Exact{}, err
	}
	return exactFromBig(new(big.Rat).Sub(l, r)), nil
}

func exactMul(left, right Exact) (Exact, error) {
	l, err := left.rat()
	if err != nil {
		return Exact{}, err
	}
	r, err := right.rat()
	if err != nil {
		return Exact{}, err
	}
	return exactFromBig(new(big.Rat).Mul(l, r)), nil
}

func exactQuo(left, right Exact) (Exact, error) {
	l, err := left.rat()
	if err != nil {
		return Exact{}, err
	}
	r, err := right.rat()
	if err != nil || r.Sign() == 0 {
		return Exact{}, runtimeError(CodeInputInvalid, "division", ErrInputInvalid)
	}
	return exactFromBig(new(big.Rat).Quo(l, r)), nil
}

func exactAbs(value Exact) (Exact, error) {
	parsed, err := value.rat()
	if err != nil {
		return Exact{}, err
	}
	return exactFromBig(parsed.Abs(parsed)), nil
}

func exactMin(left, right Exact) (Exact, error) {
	l, err := left.rat()
	if err != nil {
		return Exact{}, err
	}
	r, err := right.rat()
	if err != nil {
		return Exact{}, err
	}
	if l.Cmp(r) <= 0 {
		return exactFromBig(l), nil
	}
	return exactFromBig(r), nil
}

// StereoSample is the graph's exact, pre-quantization output domain.
type StereoSample struct {
	Left  Exact `json:"left"`
	Right Exact `json:"right"`
}

func zeroStereo() StereoSample { return StereoSample{Left: Zero(), Right: Zero()} }

func addStereo(left, right StereoSample) (StereoSample, error) {
	l, err := exactAdd(left.Left, right.Left)
	if err != nil {
		return StereoSample{}, err
	}
	r, err := exactAdd(left.Right, right.Right)
	if err != nil {
		return StereoSample{}, err
	}
	return StereoSample{Left: l, Right: r}, nil
}

func gainStereo(value StereoSample, gain Exact) (StereoSample, error) {
	left, err := exactMul(value.Left, gain)
	if err != nil {
		return StereoSample{}, err
	}
	right, err := exactMul(value.Right, gain)
	if err != nil {
		return StereoSample{}, err
	}
	return StereoSample{Left: left, Right: right}, nil
}

func panStereo(value StereoSample, pan Exact) (StereoSample, error) {
	one := One()
	oneMinusPan, err := exactSub(one, pan)
	if err != nil {
		return StereoSample{}, err
	}
	onePlusPan, err := exactAdd(one, pan)
	if err != nil {
		return StereoSample{}, err
	}
	leftGain, err := exactMin(one, oneMinusPan)
	if err != nil {
		return StereoSample{}, err
	}
	rightGain, err := exactMin(one, onePlusPan)
	if err != nil {
		return StereoSample{}, err
	}
	left, err := exactMul(value.Left, leftGain)
	if err != nil {
		return StereoSample{}, err
	}
	right, err := exactMul(value.Right, rightGain)
	if err != nil {
		return StereoSample{}, err
	}
	return StereoSample{Left: left, Right: right}, nil
}

func limitStereo(value StereoSample, ceiling Exact) (StereoSample, error) {
	leftPeak, err := exactAbs(value.Left)
	if err != nil {
		return StereoSample{}, err
	}
	rightPeak, err := exactAbs(value.Right)
	if err != nil {
		return StereoSample{}, err
	}
	peak := leftPeak
	leftRat, _ := leftPeak.rat()
	rightRat, _ := rightPeak.rat()
	if rightRat.Cmp(leftRat) > 0 {
		peak = rightPeak
	}
	if peak.Sign() == 0 {
		return value, nil
	}
	gain, err := exactQuo(ceiling, peak)
	if err != nil {
		return StereoSample{}, err
	}
	gain, err = exactMin(One(), gain)
	if err != nil {
		return StereoSample{}, err
	}
	return gainStereo(value, gain)
}
