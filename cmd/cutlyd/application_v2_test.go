package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
)

func TestNativeV2FlagsFailClosedAndLegacyDefault(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.nativeV2.enabled || c.render.enabled {
		t.Fatal("legacy defaults changed")
	}
	for _, args := range [][]string{{"--render-native-v2"}, {"--render-native-v2-worker=/tmp/worker"}, {"--render-native-v2-host-config=/tmp/host.json"}} {
		if _, err := parseConfig(args); err == nil {
			t.Fatal("unconfigured native flags accepted", args)
		}
	}
}

func TestNativeV2CapabilitiesIncludeSupervisorAuthority(t *testing.T) {
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	c := config{render: renderConfig{sdkBuildDigest: digest("a")}}
	base, err := nativeCapabilitiesV2(c)
	if err != nil {
		t.Fatal(err)
	}
	wantBase, err := ffmpegbackend.ChunkAssemblerCapabilitiesV2(c.render.sdkBuildDigest)
	if err != nil || !reflect.DeepEqual(base, wantBase) {
		t.Fatalf("base capabilities err=%v got=%+v want=%+v", err, base, wantBase)
	}
	c.nativeV2.supervisorExecutable = "/host/supervisor"
	c.nativeV2.supervisorDigest = digest("b")
	c.nativeV2.supervisorDependencies = digest("c")
	supervised, err := nativeCapabilitiesV2(c)
	if err != nil {
		t.Fatal(err)
	}
	wantSupervised, err := ffmpegbackend.ChunkAssemblerSupervisedCapabilitiesV2(c.render.sdkBuildDigest, c.nativeV2.supervisorDigest, c.nativeV2.supervisorDependencies)
	if err != nil || !reflect.DeepEqual(supervised, wantSupervised) || reflect.DeepEqual(supervised, base) {
		t.Fatalf("supervised capabilities err=%v got=%+v want=%+v", err, supervised, wantSupervised)
	}
}
