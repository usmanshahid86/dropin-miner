package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ReleaseVersion is the X.Y.Z a vX.Y.Z tag names.
//
// The tag carries the v and the version does not, and the whole release
// depends on that asymmetry holding: goreleaser strips the prefix into
// -X main.version, npm/package.json stores the bare X.Y.Z, and
// npm/install.js puts the v back to build the download URL. Keeping the
// two forms in one type — Tag() and String() — is what stops a caller
// from guessing which one it holds.
type ReleaseVersion struct {
	Major int
	Minor int
	Patch int
}

// String is the npm form: 0.2.7.
func (v ReleaseVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Tag is the git form: v0.2.7.
func (v ReleaseVersion) Tag() string { return "v" + v.String() }

// ParseReleaseTag accepts exactly vX.Y.Z and nothing else.
//
// Deliberately narrower than semver. A pre-release or build-metadata tag
// (v1.2.3-rc1, v1.2.3+build) would sail through goreleaser and produce a
// GitHub Release, but npm/package.json would have to carry the same
// string for the wrapper to resolve, and nothing downstream of here has
// ever been exercised against one. Refusing it is the honest answer
// until someone actually wants pre-releases and works through what they
// mean for the npm dist-tag; silently accepting one would publish an
// rc to npm's `latest`, which is the failure this rejects.
//
// Leading zeros are refused for the same reason: v0.02.7 and v0.2.7 are
// different tags that produce the same npm version, so one of them is a
// typo and there is no way to tell which.
func ParseReleaseTag(tag string) (ReleaseVersion, error) {
	if tag == "" {
		return ReleaseVersion{}, errors.New("empty tag")
	}
	if !strings.HasPrefix(tag, "v") {
		return ReleaseVersion{}, fmt.Errorf("tag %q does not start with %q: a release tag is vX.Y.Z", tag, "v")
	}
	fields := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(fields) != 3 {
		return ReleaseVersion{}, fmt.Errorf("tag %q has %d dot-separated fields, want 3 (vX.Y.Z)", tag, len(fields))
	}
	var out [3]int
	for i, f := range fields {
		n, err := parseNumericField(f)
		if err != nil {
			return ReleaseVersion{}, fmt.Errorf("tag %q: %w", tag, err)
		}
		out[i] = n
	}
	return ReleaseVersion{Major: out[0], Minor: out[1], Patch: out[2]}, nil
}

func parseNumericField(f string) (int, error) {
	if f == "" {
		return 0, errors.New("empty version field")
	}
	for _, r := range f {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("version field %q is not a plain number: pre-release and build metadata are not accepted", f)
		}
	}
	if len(f) > 1 && f[0] == '0' {
		return 0, fmt.Errorf("version field %q has a leading zero", f)
	}
	n, err := strconv.Atoi(f)
	if err != nil {
		return 0, fmt.Errorf("version field %q: %w", f, err)
	}
	return n, nil
}
