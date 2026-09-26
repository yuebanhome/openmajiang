package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"testing"

	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestRegisteredRulePinsLoadedExecutableAndKeepsSourceIdentity(t *testing.T) {
	rule := mcr.New()
	original := rule.Manifest()
	r := New()
	if err := r.Register(rule); err != nil {
		t.Fatal(err)
	}
	bound, ok := r.Get(original.ID, original.Version)
	if !ok {
		t.Fatal("missing registered rule")
	}
	m := bound.Manifest()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(body)
	if m.ArtifactHash != "sha256:"+hex.EncodeToString(expected[:]) || m.ArtifactHash == original.ArtifactHash {
		t.Fatal("registered rule is not pinned to the actual executable")
	}
	if m.SourceHash != original.ArtifactHash || m.Build == nil || m.Build.GoVersion != runtime.Version() || m.Build.Architecture != runtime.GOARCH {
		t.Fatal("source/build identity lost during artifact binding")
	}
	if rule.Manifest().ArtifactHash != original.ArtifactHash {
		t.Fatal("binding mutated pure plugin")
	}
	if len(r.RuleMap()) != 1 || r.List()[0].ArtifactHash != m.ArtifactHash {
		t.Fatal("catalog and host disagree")
	}
}
