package textraster

import (
	"math/big"
)

func addSegmentCrossings(exact exactContext, segment rasterSegment, sampleY int64, sampleColumns int, delta []int64, path string) error {
	switch segment.kind {
	case segmentLine:
		return addLineCrossing(exact, segment.p0, segment.p1, sampleY, sampleColumns, delta, path)
	case segmentQuad:
		return addQuadraticCrossings(exact, segment, sampleY, sampleColumns, delta, path)
	default:
		return rasterError(CodeInvariantFailed, path, ErrInvariantFailed)
	}
}

func addLineCrossing(exact exactContext, p0, p1 rasterPoint, sampleY int64, sampleColumns int, delta []int64, path string) error {
	if !((p0.y <= sampleY && sampleY < p1.y) || (p1.y <= sampleY && sampleY < p0.y)) {
		return nil
	}
	dy := new(big.Int).Sub(big.NewInt(p1.y), big.NewInt(p0.y))
	dx := new(big.Int).Sub(big.NewInt(p1.x), big.NewInt(p0.x))
	yOffset := new(big.Int).Sub(big.NewInt(sampleY), big.NewInt(p0.y))
	first := new(big.Int).Mul(big.NewInt(p0.x), dy)
	second := new(big.Int).Mul(yOffset, dx)
	if err := exact.check(dy, path); err != nil {
		return err
	}
	if err := exact.check(dx, path); err != nil {
		return err
	}
	if err := exact.check(yOffset, path); err != nil {
		return err
	}
	if err := exact.check(first, path); err != nil {
		return err
	}
	if err := exact.check(second, path); err != nil {
		return err
	}
	numerator := new(big.Int).Add(first, second)
	if err := exact.check(numerator, path); err != nil {
		return err
	}
	winding := int64(1)
	if dy.Sign() < 0 {
		dy.Neg(dy)
		numerator.Neg(numerator)
		winding = -1
	}
	crossing := rationalCrossing{numerator: *numerator, denominator: *dy}
	threshold, err := thresholdForCrossing(sampleColumns, func(sampleX int64) (bool, error) {
		return crossing.rightOf(exact, sampleX, path+"/x")
	})
	if err != nil {
		return err
	}
	addCrossingDelta(delta, threshold, winding)
	return nil
}

func addQuadraticCrossings(exact exactContext, segment rasterSegment, sampleY int64, sampleColumns int, delta []int64, path string) error {
	ay, by, cy, err := quadraticCoefficients(exact, segment.p0.y, segment.p1.y, segment.p2.y, sampleY, path+"/y")
	if err != nil {
		return err
	}
	ax, bx, cx, err := quadraticCoefficients(exact, segment.p0.x, segment.p1.x, segment.p2.x, 0, path+"/x")
	if err != nil {
		return err
	}
	if ay.Sign() == 0 {
		return addLinearParameterQuadraticCrossing(exact, ax, bx, cx, by, cy, sampleColumns, delta, path)
	}
	bSquared := new(big.Int).Mul(by, by)
	ac := new(big.Int).Mul(ay, cy)
	fourAC := new(big.Int).Lsh(ac, 2)
	if err := exact.check(bSquared, path+"/discriminant"); err != nil {
		return err
	}
	if err := exact.check(ac, path+"/discriminant"); err != nil {
		return err
	}
	if err := exact.check(fourAC, path+"/discriminant"); err != nil {
		return err
	}
	discriminant := new(big.Int).Sub(bSquared, fourAC)
	if err := exact.check(discriminant, path+"/discriminant"); err != nil {
		return err
	}
	if discriminant.Sign() < 0 {
		return nil
	}
	if discriminant.Sign() == 0 {
		// An interior double root is a tangent. A flat minimum exactly at an endpoint
		// still obeys lower-inclusive/upper-exclusive semantics.
		if ay.Sign() <= 0 {
			return nil
		}
		if by.Sign() == 0 { // t=0, curve leaves downward.
			return addIntegerCrossing(exact, segment.p0.x, sampleColumns, delta, 1, path+"/flat_start")
		}
		twiceA := new(big.Int).Lsh(new(big.Int).Set(ay), 1)
		endCondition := new(big.Int).Add(new(big.Int).Set(by), twiceA)
		if err := exact.check(twiceA, path+"/flat_end"); err != nil {
			return err
		}
		if err := exact.check(endCondition, path+"/flat_end"); err != nil {
			return err
		}
		if endCondition.Sign() == 0 { // t=1, curve arrives upward.
			return addIntegerCrossing(exact, segment.p2.x, sampleColumns, delta, -1, path+"/flat_end")
		}
		return nil
	}
	for _, rootSign := range []int64{-1, 1} {
		included, includeErr := algebraicRootIncluded(exact, ay, by, discriminant, rootSign, path+"/root")
		if includeErr != nil {
			return includeErr
		}
		if !included {
			continue
		}
		crossing, crossingErr := makeAlgebraicCrossing(exact, ax, bx, cx, ay, by, cy, discriminant, rootSign, path+"/root/x")
		if crossingErr != nil {
			return crossingErr
		}
		threshold, thresholdErr := thresholdForCrossing(sampleColumns, func(sampleX int64) (bool, error) {
			return crossing.rightOf(exact, sampleX, path+"/root/x")
		})
		if thresholdErr != nil {
			return thresholdErr
		}
		addCrossingDelta(delta, threshold, rootSign)
	}
	return nil
}

func quadraticCoefficients(exact exactContext, p0, p1, p2, offset int64, path string) (*big.Int, *big.Int, *big.Int, error) {
	twiceP1 := new(big.Int).Lsh(big.NewInt(p1), 1)
	if err := exact.check(twiceP1, path); err != nil {
		return nil, nil, nil, err
	}
	a := new(big.Int).Sub(big.NewInt(p0), twiceP1)
	if err := exact.check(a, path); err != nil {
		return nil, nil, nil, err
	}
	a.Add(a, big.NewInt(p2))
	if err := exact.check(a, path); err != nil {
		return nil, nil, nil, err
	}
	b := new(big.Int).Sub(big.NewInt(p1), big.NewInt(p0))
	if err := exact.check(b, path); err != nil {
		return nil, nil, nil, err
	}
	b.Lsh(b, 1)
	if err := exact.check(b, path); err != nil {
		return nil, nil, nil, err
	}
	c := new(big.Int).Sub(big.NewInt(p0), big.NewInt(offset))
	if err := exact.check(c, path); err != nil {
		return nil, nil, nil, err
	}
	return a, b, c, nil
}

func addLinearParameterQuadraticCrossing(exact exactContext, ax, bx, cx, by, cy *big.Int, sampleColumns int, delta []int64, path string) error {
	if by.Sign() == 0 {
		return nil
	}
	numerator := new(big.Int).Neg(new(big.Int).Set(cy))
	denominator := new(big.Int).Set(by)
	derivative := by.Sign()
	if denominator.Sign() < 0 {
		numerator.Neg(numerator)
		denominator.Neg(denominator)
	}
	comparisonZero := numerator.Sign()
	comparisonOne := numerator.Cmp(denominator)
	if comparisonZero < 0 || comparisonOne > 0 ||
		comparisonZero == 0 && derivative <= 0 || comparisonOne == 0 && derivative >= 0 {
		return nil
	}
	nSquared := new(big.Int).Mul(numerator, numerator)
	nd := new(big.Int).Mul(numerator, denominator)
	dSquared := new(big.Int).Mul(denominator, denominator)
	if err := exact.check(nSquared, path); err != nil {
		return err
	}
	if err := exact.check(nd, path); err != nil {
		return err
	}
	if err := exact.check(dSquared, path); err != nil {
		return err
	}
	termA := new(big.Int).Mul(ax, nSquared)
	termB := new(big.Int).Mul(bx, nd)
	termC := new(big.Int).Mul(cx, dSquared)
	for _, value := range []*big.Int{termA, termB, termC} {
		if err := exact.check(value, path); err != nil {
			return err
		}
	}
	xNumerator := new(big.Int).Add(termA, termB)
	if err := exact.check(xNumerator, path); err != nil {
		return err
	}
	xNumerator.Add(xNumerator, termC)
	if err := exact.check(xNumerator, path); err != nil {
		return err
	}
	crossing := rationalCrossing{numerator: *xNumerator, denominator: *dSquared}
	threshold, err := thresholdForCrossing(sampleColumns, func(sampleX int64) (bool, error) {
		return crossing.rightOf(exact, sampleX, path)
	})
	if err != nil {
		return err
	}
	winding := int64(1)
	if derivative < 0 {
		winding = -1
	}
	addCrossingDelta(delta, threshold, winding)
	return nil
}

func algebraicRootIncluded(exact exactContext, ay, by, discriminant *big.Int, rootSign int64, path string) (bool, error) {
	minusB := new(big.Int).Neg(new(big.Int).Set(by))
	rootCoefficient := big.NewInt(rootSign)
	cmpZero, err := signSqrtExpression(exact, minusB, rootCoefficient, discriminant, path+"/zero")
	if err != nil {
		return false, err
	}
	twiceA := new(big.Int).Lsh(new(big.Int).Set(ay), 1)
	if err := exact.check(twiceA, path+"/one"); err != nil {
		return false, err
	}
	minusBMinusTwoA := new(big.Int).Sub(new(big.Int).Set(minusB), twiceA)
	if err := exact.check(minusBMinusTwoA, path+"/one"); err != nil {
		return false, err
	}
	cmpOne, err := signSqrtExpression(exact, minusBMinusTwoA, rootCoefficient, discriminant, path+"/one")
	if err != nil {
		return false, err
	}
	if ay.Sign() < 0 {
		cmpZero = -cmpZero
		cmpOne = -cmpOne
	}
	if cmpZero < 0 || cmpOne > 0 {
		return false, nil
	}
	// dy/dt at this root is rootSign*sqrt(D). This freezes lower-inclusive,
	// upper-exclusive endpoint handling without evaluating an approximate root.
	if cmpZero == 0 && rootSign <= 0 || cmpOne == 0 && rootSign >= 0 {
		return false, nil
	}
	return true, nil
}

func makeAlgebraicCrossing(exact exactContext, ax, bx, cx, ay, by, cy, discriminant *big.Int, rootSign int64, path string) (algebraicCrossing, error) {
	// On y(t)=0, 2*Ay^2*x(t)=U+V*sqrt(D), with a strictly positive denominator.
	left := new(big.Int).Mul(ay, bx)
	if err := exact.check(left, path); err != nil {
		return algebraicCrossing{}, err
	}
	right := new(big.Int).Mul(ax, by)
	if err := exact.check(right, path); err != nil {
		return algebraicCrossing{}, err
	}
	l := new(big.Int).Sub(left, right)
	if err := exact.check(l, path); err != nil {
		return algebraicCrossing{}, err
	}
	left = new(big.Int).Mul(ay, cx)
	if err := exact.check(left, path); err != nil {
		return algebraicCrossing{}, err
	}
	right = new(big.Int).Mul(ax, cy)
	if err := exact.check(right, path); err != nil {
		return algebraicCrossing{}, err
	}
	m := new(big.Int).Sub(left, right)
	if err := exact.check(m, path); err != nil {
		return algebraicCrossing{}, err
	}
	uFirst := new(big.Int).Mul(l, by)
	if err := exact.check(uFirst, path); err != nil {
		return algebraicCrossing{}, err
	}
	uFirst.Neg(uFirst)
	uSecond := new(big.Int).Mul(ay, m)
	if err := exact.check(uSecond, path); err != nil {
		return algebraicCrossing{}, err
	}
	uSecond.Lsh(uSecond, 1)
	if err := exact.check(uFirst, path); err != nil {
		return algebraicCrossing{}, err
	}
	if err := exact.check(uSecond, path); err != nil {
		return algebraicCrossing{}, err
	}
	u := new(big.Int).Add(uFirst, uSecond)
	if err := exact.check(u, path); err != nil {
		return algebraicCrossing{}, err
	}
	v := new(big.Int).Mul(l, big.NewInt(rootSign))
	if err := exact.check(v, path); err != nil {
		return algebraicCrossing{}, err
	}
	denominator := new(big.Int).Mul(ay, ay)
	if err := exact.check(denominator, path); err != nil {
		return algebraicCrossing{}, err
	}
	denominator.Lsh(denominator, 1)
	if err := exact.check(denominator, path); err != nil {
		return algebraicCrossing{}, err
	}
	return algebraicCrossing{u: *u, v: *v, discriminant: *new(big.Int).Set(discriminant), denominator: *denominator}, nil
}

func addCrossingDelta(delta []int64, threshold int, winding int64) {
	if threshold <= 0 {
		return
	}
	delta[0] += winding
	delta[threshold] -= winding
}

func addIntegerCrossing(exact exactContext, x int64, sampleColumns int, delta []int64, winding int64, path string) error {
	crossing := rationalCrossing{numerator: *big.NewInt(x), denominator: *big.NewInt(1)}
	threshold, err := thresholdForCrossing(sampleColumns, func(sampleX int64) (bool, error) {
		return crossing.rightOf(exact, sampleX, path)
	})
	if err != nil {
		return err
	}
	addCrossingDelta(delta, threshold, winding)
	return nil
}
