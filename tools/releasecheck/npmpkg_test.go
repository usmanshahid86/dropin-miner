package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckNPMPackageVersionAcceptsTheAlignedManifest(t *testing.T) {
	manifest := []byte(`{"name":"dropin-miner","version":"0.2.8","bin":{"dropin-miner":"bin/dropin-miner.js"}}`)
	if err := CheckNPMPackageVersion(manifest, mustParseTag(t, "v0.2.8")); err != nil {
		t.Errorf("an aligned manifest was rejected: %v", err)
	}
}

// This is v0.2.1 and v0.2.2 reproduced: the bump landed one commit late,
// so the tagged commit still carried the previous version. Both shipped.
func TestCheckNPMPackageVersionRejectsTheBumpThatLandedLate(t *testing.T) {
	manifest := []byte(`{"name":"dropin-miner","version":"0.2.7"}`)
	err := CheckNPMPackageVersion(manifest, mustParseTag(t, "v0.2.8"))
	if err == nil {
		t.Fatal("a manifest one version behind the tag was accepted; this is exactly how v0.2.1 and v0.2.2 shipped")
	}
	if !strings.Contains(err.Error(), "0.2.7") || !strings.Contains(err.Error(), "0.2.8") {
		t.Errorf("the error names neither side of the mismatch: %v", err)
	}
}

func TestCheckNPMPackageVersionRejectsAManifestItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, manifest string
	}{
		{"not JSON", "{"},
		{"no version field", `{"name":"dropin-miner"}`},
		{"an empty version", `{"name":"dropin-miner","version":""}`},
		{"a v-prefixed version, which npm would not resolve", `{"version":"v0.2.8"}`},
		{"a version with a leading space", `{"version":" 0.2.8"}`},
	} {
		if err := CheckNPMPackageVersion([]byte(tc.manifest), mustParseTag(t, "v0.2.8")); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// Unknown keys must not be a release failure: the wrapper's manifest
// carries bin, files, engines, os and cpu today and will grow more.
func TestNPMPackageVersionReadsTheRealManifest(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("..", "..", "npm", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := NPMPackageVersion(manifest)
	if err != nil {
		t.Fatalf("the repository's own npm/package.json was not readable: %v", err)
	}
	if got == "" {
		t.Fatal("npm/package.json read as an empty version")
	}
	if _, err := ParseReleaseTag("v" + got); err != nil {
		t.Errorf("npm/package.json says version %q, which is not a shape a release tag could carry: %v", got, err)
	}
}
