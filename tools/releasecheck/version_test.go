package main

import "testing"

func TestParseReleaseTagAcceptsExactlyVMajorMinorPatch(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want string
	}{
		{"v0.2.8", "0.2.8"},
		{"v0.0.0", "0.0.0"},
		{"v1.0.0", "1.0.0"},
		{"v0.2.10", "0.2.10"},
		{"v10.20.30", "10.20.30"},
	} {
		got, err := ParseReleaseTag(tc.tag)
		if err != nil {
			t.Errorf("ParseReleaseTag(%q): %v", tc.tag, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseReleaseTag(%q).String() = %q, want %q", tc.tag, got, tc.want)
		}
		if got.Tag() != tc.tag {
			t.Errorf("ParseReleaseTag(%q).Tag() = %q, want the tag back", tc.tag, got.Tag())
		}
	}
}

// The rejections are the reason this parser exists. Each entry here is a
// ref name somebody could plausibly push, and every one of them would
// otherwise reach goreleaser and produce a GitHub Release under a name
// npm/install.js cannot resolve.
func TestParseReleaseTagRejectsEverythingElse(t *testing.T) {
	for _, tc := range []struct {
		tag, why string
	}{
		{"", "an empty ref"},
		{"0.2.8", "no v prefix: install.js downloads from releases/download/v${version}"},
		{"V0.2.8", "an upper-case V is a different ref than the one the installers look for"},
		{"v0.2", "two fields is not a release version"},
		{"v0.2.8.1", "four fields is not a release version"},
		{"v0.2.8-rc1", "a pre-release would publish to npm's latest dist-tag"},
		{"v0.2.8+build.5", "build metadata has no npm equivalent"},
		{"v0.02.8", "a leading zero makes two tags share one npm version"},
		{"v0.2.x", "a non-numeric field"},
		{"v0.2.", "an empty trailing field"},
		{"v.2.8", "an empty leading field"},
		{"v0.2.8 ", "trailing whitespace is a different ref"},
		{"latest", "a moving name is not a release"},
		{"v-1.2.3", "a negative major"},
	} {
		if got, err := ParseReleaseTag(tc.tag); err == nil {
			t.Errorf("ParseReleaseTag(%q) = %v, want an error: %s", tc.tag, got, tc.why)
		}
	}
}
