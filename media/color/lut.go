// Package color LUT support: parses .cube LUT files and applies them as a
// post-grade stage in the output color pipeline.
package color

import (
	"bufio"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LUT is a 3D lookup table parsed from an Adobe .cube file. It maps input RGB
// (0.0-1.0) to output RGB (0.0-1.0) through trilinear interpolation.
type LUT struct {
	Title  string
	Size   int
	Values [][3]float32
}

// ParseLUT reads an Adobe .cube format LUT file.
func ParseLUT(data []byte) (*LUT, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lut := &LUT{}
	var values [][3]float32
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "TITLE":
			if len(fields) >= 2 {
				lut.Title = strings.Trim(fields[1], `"`)
			}
		case "LUT_3D_SIZE":
			if len(fields) >= 2 {
				size, err := strconv.Atoi(fields[1])
				if err != nil || size < 2 || size > 129 {
					return nil, fmt.Errorf("color: LUT_3D_SIZE %q invalid", fields[1])
				}
				lut.Size = size
			}
		case "DOMAIN_MIN", "DOMAIN_MAX":
			// Parsed but not enforced; standard domain is 0..1.
		default:
			// Data line: R G B (three floats)
			if len(fields) >= 3 {
				r, err1 := strconv.ParseFloat(fields[0], 32)
				g, err2 := strconv.ParseFloat(fields[1], 32)
				b, err3 := strconv.ParseFloat(fields[2], 32)
				if err1 != nil || err2 != nil || err3 != nil {
					continue
				}
				values = append(values, [3]float32{float32(r), float32(g), float32(b)})
			}
		}
	}
	if lut.Size < 2 {
		return nil, fmt.Errorf("color: LUT missing LUT_3D_SIZE")
	}
	expected := lut.Size * lut.Size * lut.Size
	if len(values) != expected {
		return nil, fmt.Errorf("color: LUT has %d values, want %d", len(values), expected)
	}
	lut.Values = values
	return lut, nil
}

// Apply maps one input pixel through the LUT with trilinear interpolation.
// Input and output are in 0.0-1.0 normalized space.
func (lut *LUT) Apply(r, g, b float32) (float32, float32, float32) {
	size := float32(lut.Size)
	// Map 0..1 to 0..size-1
	fr := r * (size - 1)
	fg := g * (size - 1)
	fb := b * (size - 1)

	r0 := int32(math.Floor(float64(fr)))
	g0 := int32(math.Floor(float64(fg)))
	b0 := int32(math.Floor(float64(fb)))
	r1 := r0 + 1
	g1 := g0 + 1
	b1 := b0 + 1
	if r1 >= int32(lut.Size) {
		r1 = int32(lut.Size) - 1
	}
	if g1 >= int32(lut.Size) {
		g1 = int32(lut.Size) - 1
	}
	if b1 >= int32(lut.Size) {
		b1 = int32(lut.Size) - 1
	}
	rd := fr - float32(r0)
	gd := fg - float32(g0)
	bd := fb - float32(b0)

	// .cube files use blue-fastest ordering: index = r*size*size + g*size + b
	idx := func(rr, gg, bb int32) int {
		return int(rr)*lut.Size*lut.Size + int(gg)*lut.Size + int(bb)
	}

	// Trilinear interpolation
	c000 := lut.Values[idx(r0, g0, b0)]
	c100 := lut.Values[idx(r1, g0, b0)]
	c010 := lut.Values[idx(r0, g1, b0)]
	c110 := lut.Values[idx(r1, g1, b0)]
	c001 := lut.Values[idx(r0, g0, b1)]
	c101 := lut.Values[idx(r1, g0, b1)]
	c011 := lut.Values[idx(r0, g1, b1)]
	c111 := lut.Values[idx(r1, g1, b1)]

	lerp := func(a, b, t float32) float32 { return a + (b-a)*t }

	or00 := lerp(c000[0], c100[0], rd)
	og00 := lerp(c000[1], c100[1], rd)
	ob00 := lerp(c000[2], c100[2], rd)
	or10 := lerp(c010[0], c110[0], rd)
	og10 := lerp(c010[1], c110[1], rd)
	ob10 := lerp(c010[2], c110[2], rd)
	or01 := lerp(c001[0], c101[0], rd)
	og01 := lerp(c001[1], c101[1], rd)
	ob01 := lerp(c001[2], c101[2], rd)
	or11 := lerp(c011[0], c111[0], rd)
	og11 := lerp(c011[1], c111[1], rd)
	ob11 := lerp(c011[2], c111[2], rd)

	or0 := lerp(or00, or10, gd)
	og0 := lerp(og00, og10, gd)
	ob0 := lerp(ob00, ob10, gd)
	or1 := lerp(or01, or11, gd)
	og1 := lerp(og01, og11, gd)
	ob1 := lerp(ob01, ob11, gd)

	return lerp(or0, or1, bd), lerp(og0, og1, bd), lerp(ob0, ob1, bd)
}

// IdentityLUT returns a pass-through LUT for testing.
func IdentityLUT(size int) *LUT {
	lut := &LUT{Size: size, Title: "identity"}
	lut.Values = make([][3]float32, size*size*size)
	for r := 0; r < size; r++ {
		for g := 0; g < size; g++ {
			for b := 0; b < size; b++ {
				lut.Values[r*size*size+g*size+b] = [3]float32{
					float32(r) / float32(size-1),
					float32(g) / float32(size-1),
					float32(b) / float32(size-1),
				}
			}
		}
	}
	return lut
}
