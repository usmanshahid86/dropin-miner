package main

import (
	"fmt"
	"sort"
	"strings"
)

// The npm wrapper's half of the naming contract.
//
// npm/install.js builds the download URL itself, from literals of its
// own, and never reads .goreleaser.yaml. So there are two independent
// statements of what a release asset is called, and a release is only
// installable while they agree. They have agreed by coincidence so far.
//
// What follows restates install.js's rule in Go so it can be compared
// against the goreleaser-derived set. A restatement is only worth
// anything while the thing it restates still says what it says, which is
// what installerFragments is for: the exact lines install.js builds the
// name from, asserted present before the restatement is used. Rename an
// archive on either side and the two sets stop matching; rewrite
// install.js's rule and the fragments stop matching. Either way it fails
// here, before publication, instead of at a participant's `npm install`.
//
// AGENTS.md's testing discipline calls this shape out: a source-reading
// check cannot be injection-checked with -overlay, because it parses the
// real file on disk. installer_test.go edits a copy in a temp directory
// instead.
var installerFragments = []string{
	`const osName = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform]`,
	`const arch = { x64: "amd64", arm64: "arm64" }[process.arch]`,
	`const ext = osName === "windows" ? "zip" : "tar.gz"`,
	"const name = `dropin-miner_${pkg.version}_${osName}_${arch}.${ext}`",
	"const base = `https://github.com/${REPO}/releases/download/v${pkg.version}`",
	"get(`${base}/checksums.txt`)",
}

// installerPlatforms is install.js's os map read in the direction that
// matters here: the release-asset spellings it will ask GitHub for.
var installerPlatforms = []string{"darwin", "linux", "windows"}

// installerArches is install.js's arch map, same direction.
var installerArches = []string{"amd64", "arm64"}

// InstallerAssetNames is every file npm/install.js will fetch for
// version v, given that install.js still says what installerFragments
// says it does.
func InstallerAssetNames(installJS []byte, v ReleaseVersion) ([]string, error) {
	src := string(installJS)
	for _, fragment := range installerFragments {
		if !strings.Contains(src, fragment) {
			return nil, fmt.Errorf("npm/install.js no longer contains %q.\n"+
				"  This check restates install.js's asset-naming rule in Go so it can be compared against "+
				".goreleaser.yaml's, and that restatement is only trustworthy while install.js still reads this way.\n"+
				"  Update installerFragments and InstallerAssetNames together with install.js, in the same commit",
				fragment)
		}
	}
	var names []string
	for _, goos := range installerPlatforms {
		ext := ".tar.gz"
		if goos == "windows" {
			ext = ".zip"
		}
		for _, goarch := range installerArches {
			names = append(names, fmt.Sprintf("dropin-miner_%s_%s_%s%s", v.String(), goos, goarch, ext))
		}
	}
	names = append(names, "checksums.txt")
	sort.Strings(names)
	return names, nil
}

// CheckNamingContractsAgree is the whole point of the file: what
// goreleaser will publish and what the wrapper will download are the
// same set of names.
//
// Without this, .goreleaser.yaml could rename every archive, the release
// would build and upload cleanly, the asset check would pass against the
// renamed expectation, npm would publish — and every `npm install
// dropin-miner` would fail on a 404 from a URL install.js assembled from
// a rule nobody changed.
func CheckNamingContractsAgree(goreleaserYAML, installJS []byte, v ReleaseVersion) error {
	contract, err := ParseNamingContract(goreleaserYAML)
	if err != nil {
		return err
	}
	fromConfig, err := contract.ExpectedAssets(v)
	if err != nil {
		return err
	}
	fromInstaller, err := InstallerAssetNames(installJS, v)
	if err != nil {
		return err
	}
	d := DiffAssets(fromInstaller, fromConfig)
	if !d.Complete() {
		var parts []string
		if len(d.Missing) > 0 {
			parts = append(parts, fmt.Sprintf("npm/install.js will fetch %s, which .goreleaser.yaml does not produce",
				strings.Join(d.Missing, ", ")))
		}
		if len(d.Unexpected) > 0 {
			parts = append(parts, fmt.Sprintf(".goreleaser.yaml produces %s, which npm/install.js never fetches",
				strings.Join(d.Unexpected, ", ")))
		}
		return fmt.Errorf("the release's two naming contracts disagree: %s", strings.Join(parts, "; "))
	}
	return nil
}
