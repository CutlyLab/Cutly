package textraster

import (
	"context"
)

func rasterCoverage(ctx context.Context, exact exactContext, input boundInput) ([]uint8, error) {
	if err := contextFailure(ctx, "raster/coverage"); err != nil {
		return nil, err
	}
	width := int(input.measure.width)
	height := int(input.measure.height)
	sampleColumns := width * 8
	coverage := make([]uint8, int(input.measure.pixels))
	rowMask := make([]uint8, sampleColumns)
	delta := make([]int64, sampleColumns+1)
	for sampleRow := 0; sampleRow < height*8; sampleRow++ {
		if err := contextFailure(ctx, "raster/coverage/row"); err != nil {
			return nil, err
		}
		clear(rowMask)
		sampleY := int64(sampleRow)*8 + 4
		for placementIndex := range input.placements {
			if placementIndex&255 == 0 {
				if err := contextFailure(ctx, "raster/coverage/placement"); err != nil {
					return nil, err
				}
			}
			clear(delta)
			placement := &input.placements[placementIndex]
			for segmentIndex, segment := range placement.segments {
				if segmentIndex&1023 == 0 {
					if err := contextFailure(ctx, "raster/coverage/segments"); err != nil {
						return nil, err
					}
				}
				if err := addSegmentCrossings(exact, segment, sampleY, sampleColumns, delta, "raster/coverage/segment"); err != nil {
					return nil, err
				}
			}
			var winding int64
			for sampleColumn := 0; sampleColumn < sampleColumns; sampleColumn++ {
				if sampleColumn&8191 == 0 {
					if err := contextFailure(ctx, "raster/coverage/samples"); err != nil {
						return nil, err
					}
				}
				winding += delta[sampleColumn]
				if winding != 0 {
					rowMask[sampleColumn] = 1
				}
			}
			if winding+delta[sampleColumns] != 0 {
				return nil, rasterError(CodeInvariantFailed, "raster/coverage/winding", ErrInvariantFailed)
			}
		}
		pixelRow := sampleRow / 8
		rowOffset := pixelRow * width
		for sampleColumn, covered := range rowMask {
			if covered != 0 {
				pixel := rowOffset + sampleColumn/8
				if coverage[pixel] >= 64 {
					return nil, rasterError(CodeInvariantFailed, "raster/coverage/count", ErrInvariantFailed)
				}
				coverage[pixel]++
			}
		}
	}
	return coverage, nil
}
