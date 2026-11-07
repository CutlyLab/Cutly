package documentv2

const DescriptorCatalogVersion = 1
const DescriptorCatalogVersionV2 = 2
const DescriptorCatalogVersionV3 = 3
const DescriptorCatalogVersionV4 = 4

// SemanticDescriptorCatalog is the frozen base-v1 machine-readable discovery surface for the M2
// rendering semantics originally introduced by Document v2. Backends still advertise capabilities
// separately; additive transition scopes live in SemanticDescriptorCatalogV2.
type SemanticDescriptorCatalog struct {
	SchemaVersion  int                       `json:"schema_version"`
	AudioGraphs    []AudioGraphDescriptor    `json:"audio_graphs"`
	Properties     []PropertyDescriptor      `json:"properties"`
	Sources        []SourceDescriptor        `json:"sources"`
	Interpolations []InterpolationDescriptor `json:"interpolations"`
	Transitions    []TransitionDescriptor    `json:"transitions"`
}

// SemanticDescriptors returns the frozen base-v1 defensive snapshot in canonical registry order.
// Additive scope amendments are discoverable through SemanticDescriptorsV2.
func SemanticDescriptors() SemanticDescriptorCatalog {
	return SemanticDescriptorCatalog{
		SchemaVersion:  DescriptorCatalogVersion,
		AudioGraphs:    AudioGraphDescriptors(),
		Properties:     PropertyDescriptors(),
		Sources:        SourceDescriptors(),
		Interpolations: InterpolationDescriptors(),
		Transitions:    TransitionDescriptors(),
	}
}

// SemanticDescriptorCatalogV2 is the additive discovery surface. The v1 type and
// SemanticDescriptors bytes remain frozen for existing consumers and fixtures.
type SemanticDescriptorCatalogV2 struct {
	SchemaVersion    int                               `json:"schema_version"`
	AudioGraphs      []AudioGraphDescriptor            `json:"audio_graphs"`
	Properties       []PropertyDescriptor              `json:"properties"`
	Sources          []SourceDescriptor                `json:"sources"`
	Interpolations   []InterpolationDescriptor         `json:"interpolations"`
	Transitions      []TransitionDescriptor            `json:"transitions"`
	TransitionScopes []VectorTransitionScopeDescriptor `json:"transition_scopes"`
}

// SemanticDescriptorsV2 returns a complete defensive snapshot including additive
// transition-scope amendments that cannot be represented in the frozen v1 catalog.
func SemanticDescriptorsV2() SemanticDescriptorCatalogV2 {
	v1 := SemanticDescriptors()
	return SemanticDescriptorCatalogV2{
		SchemaVersion:    DescriptorCatalogVersionV2,
		AudioGraphs:      v1.AudioGraphs,
		Properties:       v1.Properties,
		Sources:          v1.Sources,
		Interpolations:   v1.Interpolations,
		Transitions:      v1.Transitions,
		TransitionScopes: VectorTransitionScopeDescriptors(),
	}
}

// SemanticDescriptorCatalogV3 is the additive discovery surface for Text v2. Earlier catalog
// versions and their canonical bytes remain frozen for existing consumers.
type SemanticDescriptorCatalogV3 struct {
	SchemaVersion    int                               `json:"schema_version"`
	AudioGraphs      []AudioGraphDescriptor            `json:"audio_graphs"`
	Properties       []PropertyDescriptor              `json:"properties"`
	Sources          []SourceDescriptor                `json:"sources"`
	Interpolations   []InterpolationDescriptor         `json:"interpolations"`
	Transitions      []TransitionDescriptor            `json:"transitions"`
	TransitionScopes []VectorTransitionScopeDescriptor `json:"transition_scopes"`
}

// SemanticDescriptorsV3 returns the complete defensive machine-readable catalog including the
// additive pinned-font viewport Text v2 source contract.
func SemanticDescriptorsV3() SemanticDescriptorCatalogV3 {
	v2 := SemanticDescriptorsV2()
	return SemanticDescriptorCatalogV3{
		SchemaVersion:    DescriptorCatalogVersionV3,
		AudioGraphs:      v2.AudioGraphs,
		Properties:       v2.Properties,
		Sources:          SourceDescriptorsWithTextV2(),
		Interpolations:   v2.Interpolations,
		Transitions:      v2.Transitions,
		TransitionScopes: v2.TransitionScopes,
	}
}

// SemanticDescriptorCatalogV4 adds the independently negotiable Text v2 transition scope. The
// base dissolve descriptor and every earlier catalog remain byte-frozen.
type SemanticDescriptorCatalogV4 struct {
	SchemaVersion        int                               `json:"schema_version"`
	AudioGraphs          []AudioGraphDescriptor            `json:"audio_graphs"`
	Properties           []PropertyDescriptor              `json:"properties"`
	Sources              []SourceDescriptor                `json:"sources"`
	Interpolations       []InterpolationDescriptor         `json:"interpolations"`
	Transitions          []TransitionDescriptor            `json:"transitions"`
	TransitionScopes     []VectorTransitionScopeDescriptor `json:"transition_scopes"`
	TextTransitionScopes []TextTransitionScopeDescriptor   `json:"text_transition_scopes"`
}

// SemanticDescriptorsV4 returns the complete defensive catalog including both additive source
// scopes. Separate arrays keep the frozen vector descriptor type and V1-V3 wire contracts intact.
func SemanticDescriptorsV4() SemanticDescriptorCatalogV4 {
	v3 := SemanticDescriptorsV3()
	return SemanticDescriptorCatalogV4{
		SchemaVersion: DescriptorCatalogVersionV4, AudioGraphs: v3.AudioGraphs,
		Properties: v3.Properties, Sources: v3.Sources, Interpolations: v3.Interpolations,
		Transitions: v3.Transitions, TransitionScopes: v3.TransitionScopes,
		TextTransitionScopes: TextTransitionScopeDescriptors(),
	}
}
