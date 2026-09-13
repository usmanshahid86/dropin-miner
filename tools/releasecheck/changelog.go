package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// HasReleaseHeading reports whether CHANGELOG.md carries this release's
// own heading.
//
// The convention CHANGELOG.md states about itself is "one heading per
// tag, newest first, each beginning `## vX.Y.Z — YYYY-MM-DD`", and
// beginning is the operative word: v0.2.0's heading trails a
// parenthetical after the date, and v0.1.7's carries no date at all.
// So the match is a prefix — `## vX.Y.Z` followed by the end of the line
// or by whitespace — rather than the exact dated form. Requiring the
// date would fail a release over a heading the file's own preamble
// permits; requiring only a substring would let `## v0.2.1` be satisfied
// by `## v0.2.10`, which is why the boundary after the patch number is
// checked rather than assumed.
func HasReleaseHeading(changelog []byte, v ReleaseVersion) bool {
	want := "## " + v.Tag()
	scanner := bufio.NewScanner(bytes.NewReader(changelog))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if !strings.HasPrefix(line, want) {
			continue
		}
		rest := line[len(want):]
		if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
			return true
		}
	}
	return false
}

// CheckReleaseHeading is HasReleaseHeading with the error a caller wants
// to print: a missing entry is a release shipping without the one
// participant-readable account of what changed, and the fix is a commit
// on main and a re-cut tag, not a flag.
func CheckReleaseHeading(changelog []byte, v ReleaseVersion) error {
	if HasReleaseHeading(changelog, v) {
		return nil
	}
	return fmt.Errorf("CHANGELOG.md at the tag has no %q heading: "+
		"the release's own entry is written by the release PR, before the tag exists", "## "+v.Tag())
}
