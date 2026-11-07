package sfntglyf

// These models are deliberately private until GDEF, GSUB and GPOS can be
// published as one atomic LayoutGraph contract. They retain normalized values,
// never raw table bytes or operation-local readers.

type layoutCoverage = gdefCoverage
type layoutClassDef = gdefClassDef

func layoutClassDefMax(value *layoutClassDef) uint16 {
	if value == nil {
		return 0
	}
	var maximum uint16
	for _, class := range value.values {
		if class > maximum {
			maximum = class
		}
	}
	for _, record := range value.ranges {
		if record.class > maximum {
			maximum = record.class
		}
	}
	return maximum
}

type layoutScriptRecord struct {
	tag    uint32
	script *layoutScript
}

type layoutScript struct {
	defaultLanguage *layoutLangSys
	languages       []layoutLangSysRecord
}

type layoutLangSysRecord struct {
	tag      uint32
	language *layoutLangSys
}

type layoutLangSys struct {
	requiredFeatureIndex uint16
	featureIndices       []uint16
}

type layoutFeatureRecord struct {
	tag     uint32
	feature *layoutFeature
	params  layoutFeatureParams
}

type layoutFeature struct {
	paramsOffset  uint16
	lookupIndices []uint16
}

type layoutFeatureParams interface {
	layoutFeatureParamsKind() layoutSpanKind
}

type layoutCVFeatureParams struct {
	featureUILabelNameID        uint16
	featureUITooltipTextNameID  uint16
	sampleTextNameID            uint16
	namedParameterCount         uint16
	firstParameterUILabelNameID uint16
	characters                  []uint32
}

func (*layoutCVFeatureParams) layoutFeatureParamsKind() layoutSpanKind {
	return layoutSpanFeatureParamsCV
}

type layoutSSFeatureParams struct {
	uiLabelNameID uint16
}

func (*layoutSSFeatureParams) layoutFeatureParamsKind() layoutSpanKind {
	return layoutSpanFeatureParamsSS
}

// layoutSizeFeatureParams is the five-uint16 payload of the legacy GPOS
// optical-size feature. It is retained only as validation data; STAT remains
// the modern selection mechanism and this package does not select fonts.
type layoutSizeFeatureParams struct {
	designSize      uint16
	subfamilyID     uint16
	subfamilyNameID uint16
	rangeStart      uint16
	rangeEnd        uint16
}

func (*layoutSizeFeatureParams) layoutFeatureParamsKind() layoutSpanKind {
	return layoutSpanFeatureParamsSize
}
