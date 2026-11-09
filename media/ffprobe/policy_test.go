package ffprobe

import (
	"testing"

	"github.com/CutlyLab/Cutly/media/probe"
)

func TestAdapterPolicyValues(t *testing.T) {
	var absent *Adapter
	if absent.Options() != (Options{}) || absent.Identity() != (probe.ProbeIdentity{}) {
		t.Fatal("nil adapter exposed nonzero policy")
	}
	options, err := normalizedOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{options: options, buildFingerprint: "build", optionsFingerprint: "options"}
	want := probe.ProbeIdentity{Implementation: Implementation, BuildFingerprint: "build", OptionsFingerprint: "options"}
	got := adapter.Options()
	got.MaxOutputBytes = 1
	identity := adapter.Identity()
	identity.BuildFingerprint = "changed"
	if adapter.Options() != options || adapter.Identity() != want {
		t.Fatal("returned policy aliases adapter state")
	}
}
