package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mustParseTag(t *testing.T, tag string) ReleaseVersion {
	t.Helper()
	v, err := ParseReleaseTag(tag)
	if err != nil {
		t.Fatalf("ParseReleaseTag(%q): %v", tag, err)
	}
	return v
}

func TestHasReleaseHeadingAcceptsTheFormsTheChangelogActuallyUses(t *testing.T) {
	// Every one of these is a heading shape that exists in the real
	// CHANGELOG.md today, which is why the match is a prefix and not the
	// dated form the preamble describes.
	for _, tc := range []struct {
		name, heading string
	}{
		{"dated, the usual shape", "## v0.2.8 — 2026-09-20"},
		{"dated with a trailing parenthetical, as v0.2.0 has", "## v0.2.8 — 2026-09-20 (the release that changed the install path)"},
		{"undated, as v0.1.7 has", "## v0.2.8"},
		{"a tab after the version", "## v0.2.8\t— 2026-09-20"},
	} {
		body := []byte("# Changelog\n\nSome preamble.\n\n" + tc.heading + "\n\n- a bullet\n")
		if !HasReleaseHeading(body, mustParseTag(t, "v0.2.8")) {
			t.Errorf("%s: %q was not recognized as v0.2.8's heading", tc.name, tc.heading)
		}
	}
}

// The boundary after the patch number is the whole reason this is not a
// substring search: "## v0.2.1" is a prefix of "## v0.2.10", so a
// release of 0.2.1 with no entry of its own would be waved through by
// 0.2.10's heading.
func TestHasReleaseHeadingDoesNotAcceptALongerVersion(t *testing.T) {
	body := []byte("# Changelog\n\n## v0.2.10 — 2026-10-01\n\n- ten\n")
	if HasReleaseHeading(body, mustParseTag(t, "v0.2.1")) {
		t.Error("v0.2.10's heading satisfied a check for v0.2.1: the version boundary is not being enforced")
	}
	if !HasReleaseHeading(body, mustParseTag(t, "v0.2.10")) {
		t.Error("v0.2.10's own heading was not recognized")
	}
}

func TestHasReleaseHeadingRejectsWhatIsNotAHeading(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"no entry at all", "# Changelog\n\n## v0.2.7 — 2026-09-13\n\n- seven\n"},
		{"the version only in prose", "# Changelog\n\nv0.2.8 is coming.\n\n## v0.2.7 — 2026-09-13\n"},
		{"a deeper heading", "# Changelog\n\n### v0.2.8 — 2026-09-20\n"},
		{"a shallower heading", "# Changelog\n\n# v0.2.8 — 2026-09-20\n"},
		{"an Unreleased catch-all instead of the version", "# Changelog\n\n## Unreleased\n\n- something\n"},
		{"empty", ""},
	} {
		if HasReleaseHeading([]byte(tc.body), mustParseTag(t, "v0.2.8")) {
			t.Errorf("%s: accepted as a v0.2.8 heading", tc.name)
		}
	}
}

// The convention this reads is stated by CHANGELOG.md itself, so it is
// worth proving the reader agrees with the file rather than only with
// the fixtures above.
func TestHasReleaseHeadingAgreesWithTheRealChangelog(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v0.2.7", "v0.2.6", "v0.2.0", "v0.1.7"} {
		if !HasReleaseHeading(body, mustParseTag(t, tag)) {
			t.Errorf("CHANGELOG.md has a heading for %s, but the reader did not find it", tag)
		}
	}
	// A version that has never shipped must not be found, or the check
	// would pass for every future release without anyone writing an entry.
	if HasReleaseHeading(body, mustParseTag(t, "v9.9.9")) {
		t.Error("CHANGELOG.md appears to have a v9.9.9 heading, which means this reader matches too loosely")
	}
}
