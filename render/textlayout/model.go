package textlayout

import "github.com/CutlyLab/Cutly/core/documentv2"

// Q26_6 is a signed fixed-point value with 6 fractional bits. Shaped input and glyph outlines use
// font-space Q26.6; successful Layout data uses viewport-pixel Q26.6.
type Q26_6 int64

type Point struct {
	X Q26_6 `json:"x"`
	Y Q26_6 `json:"y"`
}

type OutlineCommandOp string

const (
	MoveTo OutlineCommandOp = "move_to"
	LineTo OutlineCommandOp = "line_to"
	QuadTo OutlineCommandOp = "quad_to"
	Close  OutlineCommandOp = "close"
)

type OutlineCommand struct {
	Op     OutlineCommandOp `json:"op"`
	Points []Point          `json:"points,omitempty"`
}

type GlyphOutline struct {
	GlyphID  uint32           `json:"glyph_id"`
	Commands []OutlineCommand `json:"commands"`
}

type ShapedGlyph struct {
	GlyphID           uint32 `json:"glyph_id"`
	ClusterByteOffset uint64 `json:"cluster_byte_offset"`
	XAdvance          Q26_6  `json:"x_advance"`
	YAdvance          Q26_6  `json:"y_advance"`
	XOffset           Q26_6  `json:"x_offset"`
	YOffset           Q26_6  `json:"y_offset"`
}

type ShapedLine struct {
	ByteStart uint64        `json:"byte_start"`
	ByteEnd   uint64        `json:"byte_end"`
	Glyphs    []ShapedGlyph `json:"glyphs"`
}

// Binding carries upstream identity claims for the immutable Document/font/shaping snapshot that
// produced Input. Build validates canonical spellings and re-hashes the concrete supplied glyph
// stream, but it has neither the source Document/font bytes nor a provider trust root and therefore
// does not authenticate these claims. Assembly must derive and live-check them at its boundary.
type Binding struct {
	TextSemanticHashProfile  string `json:"text_semantic_hash_profile"`
	TextSemanticHash         string `json:"text_semantic_hash"`
	LayoutInputHashProfile   string `json:"layout_input_hash_profile"`
	LayoutInputHash          string `json:"layout_input_hash"`
	FontContentHash          string `json:"font_content_hash"`
	UnicodeTableProfile      string `json:"unicode_table_profile"`
	UnicodeTableHashProfile  string `json:"unicode_table_hash_profile"`
	UnicodeTableHash         string `json:"unicode_table_hash"`
	ShapeProviderProfile     string `json:"shape_provider_profile"`
	ShapeProviderFingerprint string `json:"shape_provider_fingerprint"`
}

type Input struct {
	TextProfile         string                    `json:"text_profile"`
	RunInputProfile     string                    `json:"run_input_profile"`
	ShapedStreamProfile string                    `json:"shaped_stream_profile"`
	LayoutProfile       string                    `json:"layout_profile"`
	FontProfile         string                    `json:"font_profile"`
	OutlineProfile      string                    `json:"outline_profile"`
	Text                string                    `json:"text"`
	Viewport            documentv2.RasterViewport `json:"viewport"`
	FontSize            documentv2.Rational       `json:"font_size"`
	LineHeight          documentv2.Rational       `json:"line_height"`
	Language            string                    `json:"language"`
	Script              string                    `json:"script"`
	Direction           documentv2.TextDirection  `json:"direction"`
	Alignment           documentv2.TextAlignment  `json:"alignment"`
	UnitsPerEm          uint32                    `json:"units_per_em"`
	HheaAscender        int32                     `json:"hhea_ascender"`
	Lines               []ShapedLine              `json:"lines"`
	Outlines            []GlyphOutline            `json:"outlines"`
	Binding             Binding                   `json:"binding"`
}

type GlyphPlacement struct {
	LineIndex         uint32 `json:"line_index"`
	GlyphIndex        uint64 `json:"glyph_index"`
	GlyphID           uint32 `json:"glyph_id"`
	ClusterByteOffset uint64 `json:"cluster_byte_offset"`
	OriginX           Q26_6  `json:"origin_x"`
	BaselineY         Q26_6  `json:"baseline_y"`
}

type CommandInfo struct {
	Op         OutlineCommandOp
	PointCount int
}

type layoutCommand struct {
	opTag      uint8
	pointCount uint8
	points     [2]Point
}

type layoutOutline struct {
	glyphID  uint32
	commands []layoutCommand
}

type Layout struct {
	viewport   documentv2.RasterViewport
	fontSize   documentv2.Rational
	placements []GlyphPlacement
	outlines   []layoutOutline
	receipt    Receipt
}

type ReadView struct{ layout *Layout }

type OutlineView struct {
	layout *Layout
	index  int
}

func (value *Layout) View() (ReadView, error) {
	if value == nil {
		return ReadView{}, layoutError(CodeInputInvalid, "layout", ErrInputInvalid)
	}
	return ReadView{layout: value}, nil
}

func (value *Layout) Receipt() Receipt {
	if value == nil {
		return Receipt{}
	}
	return value.receipt
}

// FontSize returns the exact font-size value used to build this immutable Layout. Retaining the
// value lets a downstream live-binding adapter distinguish layouts whose scaled outlines happen
// to be identical, such as empty or all-zero glyphs.
func (value *Layout) FontSize() documentv2.Rational {
	if value == nil {
		return documentv2.Rational{}
	}
	return value.fontSize
}

func (value ReadView) Viewport() documentv2.RasterViewport {
	if value.layout == nil {
		return documentv2.RasterViewport{}
	}
	return value.layout.viewport
}

func (value ReadView) PlacementCount() int {
	if value.layout == nil {
		return 0
	}
	return len(value.layout.placements)
}

func (value ReadView) Placement(index int) (GlyphPlacement, error) {
	if value.layout == nil || index < 0 || index >= len(value.layout.placements) {
		return GlyphPlacement{}, layoutError(CodeInputInvalid, "layout/placements", ErrInputInvalid)
	}
	return value.layout.placements[index], nil
}

func (value ReadView) OutlineCount() int {
	if value.layout == nil {
		return 0
	}
	return len(value.layout.outlines)
}

func (value ReadView) OutlineAt(index int) (OutlineView, error) {
	if value.layout == nil || index < 0 || index >= len(value.layout.outlines) {
		return OutlineView{}, layoutError(CodeInputInvalid, "layout/outlines", ErrInputInvalid)
	}
	return OutlineView{layout: value.layout, index: index}, nil
}

func (value ReadView) Outline(glyphID uint32) (OutlineView, error) {
	if value.layout == nil || glyphID == 0 {
		return OutlineView{}, layoutError(CodeInputInvalid, "layout/outlines/glyph_id", ErrInputInvalid)
	}
	left, right := 0, len(value.layout.outlines)
	for left < right {
		middle := left + (right-left)/2
		if value.layout.outlines[middle].glyphID < glyphID {
			left = middle + 1
		} else {
			right = middle
		}
	}
	if left >= len(value.layout.outlines) || value.layout.outlines[left].glyphID != glyphID {
		return OutlineView{}, layoutError(CodeInputInvalid, "layout/outlines/glyph_id", ErrInputInvalid)
	}
	return OutlineView{layout: value.layout, index: left}, nil
}

func (value OutlineView) GlyphID() uint32 {
	if value.layout == nil || value.index < 0 || value.index >= len(value.layout.outlines) {
		return 0
	}
	return value.layout.outlines[value.index].glyphID
}

func (value OutlineView) CommandCount() int {
	if value.layout == nil || value.index < 0 || value.index >= len(value.layout.outlines) {
		return 0
	}
	return len(value.layout.outlines[value.index].commands)
}

func (value OutlineView) Command(index int) (CommandInfo, error) {
	if value.layout == nil || value.index < 0 || value.index >= len(value.layout.outlines) ||
		index < 0 || index >= len(value.layout.outlines[value.index].commands) {
		return CommandInfo{}, layoutError(CodeInputInvalid, "layout/outlines/commands", ErrInputInvalid)
	}
	command := value.layout.outlines[value.index].commands[index]
	return CommandInfo{Op: outlineOpFromTag(command.opTag), PointCount: int(command.pointCount)}, nil
}

func (value OutlineView) Point(commandIndex, pointIndex int) (Point, error) {
	if value.layout == nil || value.index < 0 || value.index >= len(value.layout.outlines) ||
		commandIndex < 0 || commandIndex >= len(value.layout.outlines[value.index].commands) {
		return Point{}, layoutError(CodeInputInvalid, "layout/outlines/commands", ErrInputInvalid)
	}
	command := value.layout.outlines[value.index].commands[commandIndex]
	if pointIndex < 0 || pointIndex >= int(command.pointCount) {
		return Point{}, layoutError(CodeInputInvalid, "layout/outlines/commands/points", ErrInputInvalid)
	}
	return command.points[pointIndex], nil
}
