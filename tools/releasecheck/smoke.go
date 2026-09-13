package main

import (
	"fmt"
	"strings"
)

// CheckVersionOutput is the last link in the release chain and the only
// check that has run the released binary.
//
// Everything before it compares one version string to another: the tag
// against npm/package.json, the release's asset names against the
// config, npm's metadata against the tag. All three can pass on a
// release whose published wrapper downloads the wrong archive, or the
// right archive built from the wrong commit. This one installs the
// published package, lets its postinstall fetch and checksum the real
// archive, and asks the binary that came out what it is.
//
// The expected form is `dropin-miner X.Y.Z` — the binary name, then the
// version with no v. main.go prints `fmt.Fprintln(os.Stdout,
// "dropin-miner", buildVersion())`, and goreleaser injects
// -X main.version={{.Version}}, which resolves to the tag with the
// prefix stripped. Expecting vX.Y.Z here fails a release that is in fact
// correct, which is why the shape is pinned rather than pattern-matched.
//
// A parenthetical suffix is rejected along with everything else: the
// `X.Y.Z (rev)` form is what buildVersion produces when main.version was
// never set, so seeing it means the binary under test was not built by
// the release.
func CheckVersionOutput(out []byte, v ReleaseVersion) error {
	got := strings.TrimSpace(string(out))
	want := "dropin-miner " + v.String()
	if got == want {
		return nil
	}
	detail := ""
	switch {
	case got == "":
		detail = "\n  Nothing was printed: the wrapper found no binary to run, or it ran and printed nothing."
	case got == "dropin-miner "+v.Tag():
		detail = "\n  The v belongs to the tag, not to the binary: goreleaser strips it from -X main.version."
	case strings.Contains(got, "("):
		detail = "\n  The parenthetical form is what an un-stamped build prints, so this binary did not come from the release."
	case strings.Contains(got, "\n"):
		detail = "\n  More than one line was captured: the postinstall's own output may have been recorded along with the version."
	}
	return fmt.Errorf("the installed binary reports %q, want %q%s", got, want, detail)
}
