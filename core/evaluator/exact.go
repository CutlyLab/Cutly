package evaluator

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// ExactRational is an immutable arbitrary-precision runtime value. Document rationals use int64
// for portable storage, but a Q0.32 cubic-bezier evaluation can legitimately produce a reduced
// denominator larger than int64. Keeping that exact value here prevents platform rounding and
// lets the eventual compositor choose an explicitly versioned quantization boundary.
//
// Its JSON representation uses decimal strings so canonical evaluation fixtures remain lossless:
// {"numerator":"1","denominator":"2"}.
type ExactRational struct {
	numerator   big.Int
	denominator big.Int
}

func exactFromBig(value *big.Rat) ExactRational {
	var result ExactRational
	if value == nil {
		return result
	}
	result.numerator.Set(value.Num())
	result.denominator.Set(value.Denom())
	return result
}

func exactFromDocument(value documentv2.Rational) (ExactRational, error) {
	if err := value.Validate(); err != nil {
		return ExactRational{}, evaluationError(CodeInputInvalid, "rational", ErrInputInvalid)
	}
	return exactFromBig(new(big.Rat).SetFrac64(value.Numerator, value.Denominator)), nil
}

func exactInteger(value int64) ExactRational {
	return exactFromBig(new(big.Rat).SetInt64(value))
}

func (value ExactRational) valid() bool {
	return value.denominator.Sign() > 0
}

func (value ExactRational) big() (*big.Rat, error) {
	if !value.valid() {
		return nil, evaluationError(CodeInputInvalid, "exact_rational", ErrInputInvalid)
	}
	return new(big.Rat).SetFrac(
		new(big.Int).Set(&value.numerator),
		new(big.Int).Set(&value.denominator),
	), nil
}

// Numerator returns a defensive arbitrary-precision copy.
func (value ExactRational) Numerator() *big.Int {
	return new(big.Int).Set(&value.numerator)
}

// Denominator returns a defensive arbitrary-precision copy.
func (value ExactRational) Denominator() *big.Int {
	return new(big.Int).Set(&value.denominator)
}

func (value ExactRational) String() string {
	if !value.valid() {
		return "invalid"
	}
	return value.numerator.String() + "/" + value.denominator.String()
}

func (value ExactRational) Equal(other ExactRational) bool {
	if !value.valid() || !other.valid() {
		return false
	}
	return value.numerator.Cmp(&other.numerator) == 0 && value.denominator.Cmp(&other.denominator) == 0
}

// DocumentRational returns a portable wire value when both reduced terms fit int64. Evaluation
// itself never calls this method implicitly: truncation or rounding requires a future named
// compositor capability.
func (value ExactRational) DocumentRational() (documentv2.Rational, error) {
	if !value.valid() || !value.numerator.IsInt64() || !value.denominator.IsInt64() {
		return documentv2.Rational{}, evaluationError(CodeValueNotWireSafe, "exact_rational", ErrValueNotWireSafe)
	}
	result := documentv2.Rational{Numerator: value.numerator.Int64(), Denominator: value.denominator.Int64()}
	if err := result.Validate(); err != nil {
		return documentv2.Rational{}, evaluationError(CodeValueNotWireSafe, "exact_rational", ErrValueNotWireSafe)
	}
	return result, nil
}

func (value ExactRational) MarshalJSON() ([]byte, error) {
	if !value.valid() {
		return nil, fmt.Errorf("evaluator: cannot marshal invalid exact rational")
	}
	return json.Marshal(struct {
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
	}{Numerator: value.numerator.String(), Denominator: value.denominator.String()})
}

type ExactPoint struct {
	X ExactRational `json:"x"`
	Y ExactRational `json:"y"`
}

type ExactRect struct {
	X      ExactRational `json:"x"`
	Y      ExactRational `json:"y"`
	Width  ExactRational `json:"width"`
	Height ExactRational `json:"height"`
}

func exactPoint(value documentv2.Point) (ExactPoint, error) {
	x, err := exactFromDocument(value.X)
	if err != nil {
		return ExactPoint{}, err
	}
	y, err := exactFromDocument(value.Y)
	if err != nil {
		return ExactPoint{}, err
	}
	return ExactPoint{X: x, Y: y}, nil
}

func exactRect(value documentv2.Rect) (ExactRect, error) {
	x, err := exactFromDocument(value.X)
	if err != nil {
		return ExactRect{}, err
	}
	y, err := exactFromDocument(value.Y)
	if err != nil {
		return ExactRect{}, err
	}
	width, err := exactFromDocument(value.Width)
	if err != nil {
		return ExactRect{}, err
	}
	height, err := exactFromDocument(value.Height)
	if err != nil {
		return ExactRect{}, err
	}
	return ExactRect{X: x, Y: y, Width: width, Height: height}, nil
}
