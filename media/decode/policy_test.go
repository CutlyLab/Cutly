package videodecode

import "testing"

func TestAdapterPolicyValues(t *testing.T) {
	var absent *Adapter
	if absent.Options() != (Options{}) || absent.BuildIdentity() != (BuildIdentity{}) {
		t.Fatal("nil adapter exposed nonzero policy")
	}
	options, err := normalizeOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	identity := DefaultBuildIdentity("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	adapter := &Adapter{options: options, identity: identity}
	got := adapter.Options()
	got.MaxFrameBytes = 1
	build := adapter.BuildIdentity()
	build.RuntimeDependencyFingerprint = "changed"
	if adapter.Options() != options || adapter.BuildIdentity() != identity {
		t.Fatal("returned policy aliases adapter state")
	}
}
