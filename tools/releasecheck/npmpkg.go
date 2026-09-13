package main

import (
	"encoding/json"
	"fmt"
)

// NPMPackageVersion reads the "version" field out of npm/package.json.
//
// Decoded permissively: the wrapper's manifest grows fields (bin, files,
// os, cpu) for reasons that have nothing to do with the release check,
// and refusing an unknown key would turn an unrelated edit into a failed
// release. The one field this cares about is required to be present and
// a non-empty string.
func NPMPackageVersion(manifest []byte) (string, error) {
	var pkg struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		return "", fmt.Errorf("npm/package.json is not valid JSON: %w", err)
	}
	if pkg.Version == nil {
		return "", fmt.Errorf("npm/package.json has no %q field", "version")
	}
	if *pkg.Version == "" {
		return "", fmt.Errorf("npm/package.json has an empty %q field", "version")
	}
	return *pkg.Version, nil
}

// CheckNPMPackageVersion is the alignment invariant docs/RELEASING.md
// states and v0.2.1 and v0.2.2 both broke: the commit the tag points at
// already says X.Y.Z.
//
// Both of those shipped because the bump landed one commit late and
// nothing looked. The failure is not loud — goreleaser never reads
// npm/package.json, so the GitHub Release is perfect — it surfaces later
// as an npm package that installs the wrong binary, or as `npm publish`
// refusing a version number that is already taken. Which is why this
// runs before anything is published rather than after.
func CheckNPMPackageVersion(manifest []byte, v ReleaseVersion) error {
	got, err := NPMPackageVersion(manifest)
	if err != nil {
		return err
	}
	if got != v.String() {
		return fmt.Errorf("npm/package.json at the tag says version %q, but the tag is %s (want %q): "+
			"the bump belongs in the release PR, in the commit being tagged or an ancestor of it",
			got, v.Tag(), v.String())
	}
	return nil
}
