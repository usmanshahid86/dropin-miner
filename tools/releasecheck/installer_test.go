package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func realInstallJS(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "npm", "install.js"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func realGoreleaser(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The state the repository is actually in: the two independent
// statements of what a release asset is called agree.
func TestTheTwoNamingContractsAgreeToday(t *testing.T) {
	if err := CheckNamingContractsAgree(realGoreleaser(t), realInstallJS(t), mustParseTag(t, "v0.2.8")); err != nil {
		t.Fatalf(".goreleaser.yaml and npm/install.js no longer name the same assets: %v", err)
	}
}

func TestInstallerAssetNamesMatchesTheGoreleaserDerivation(t *testing.T) {
	got, err := InstallerAssetNames(realInstallJS(t), mustParseTag(t, "v0.2.8"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(wantAssets028, "\n") {
		t.Errorf("npm/install.js will fetch\n got %v\nwant %v", got, wantAssets028)
	}
}

// The failure this coupling exists to catch: .goreleaser.yaml renames
// every archive, the release builds and uploads perfectly, and every
// `npm install dropin-miner` 404s on a URL install.js assembled from a
// rule nobody changed.
func TestARenameOnTheGoreleaserSideIsCaught(t *testing.T) {
	renamed := strings.Replace(string(realGoreleaser(t)),
		"{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}",
		"{{ .ProjectName }}-{{ .Version }}-{{ .Os }}-{{ .Arch }}", 1)
	if renamed == string(realGoreleaser(t)) {
		t.Fatal("the name_template this test rewrites is no longer in .goreleaser.yaml; " +
			"update the rewrite so this test still exercises a rename")
	}
	err := CheckNamingContractsAgree([]byte(renamed), realInstallJS(t), mustParseTag(t, "v0.2.8"))
	if err == nil {
		t.Fatal("every archive was renamed and the check passed: the wrapper would 404 on all six platforms")
	}
	if !strings.Contains(err.Error(), "install.js") {
		t.Errorf("the error does not say which side disagrees: %v", err)
	}
}

// The other direction: the archive matrix shrinks, so a platform the
// wrapper still advertises has nothing to download.
func TestADroppedPlatformOnTheGoreleaserSideIsCaught(t *testing.T) {
	shrunk := strings.Replace(string(realGoreleaser(t)),
		"goarch: [amd64, arm64]", "goarch: [amd64]", 1)
	if shrunk == string(realGoreleaser(t)) {
		t.Fatal("the goarch line this test rewrites is no longer in .goreleaser.yaml")
	}
	err := CheckNamingContractsAgree([]byte(shrunk), realInstallJS(t), mustParseTag(t, "v0.2.8"))
	if err == nil {
		t.Fatal("arm64 was dropped from the build matrix and the check passed: " +
			"npm/package.json still declares arm64 support, so an Apple Silicon install would 404")
	}
	if !strings.Contains(err.Error(), "arm64") {
		t.Errorf("the error does not name the missing architecture: %v", err)
	}
}

// The restatement of install.js's rule is only trustworthy while
// install.js still reads the way installerFragments says. Rewriting the
// rule must stop the release, not silently keep comparing against a
// rule that no longer exists.
//
// This is a source-reading check, so it cannot be injection-checked with
// -overlay (AGENTS.md): the bytes are varied here instead of on disk.
func TestARewrittenInstallerRuleStopsTheCheck(t *testing.T) {
	for _, tc := range []struct {
		name, from, to string
	}{
		{
			name: "the archive name template",
			from: "const name = `dropin-miner_${pkg.version}_${osName}_${arch}.${ext}`",
			to:   "const name = `dropin-miner-${pkg.version}-${osName}-${arch}.${ext}`",
		},
		{
			name: "the extension rule",
			from: `const ext = osName === "windows" ? "zip" : "tar.gz"`,
			to:   `const ext = osName === "windows" ? "7z" : "tar.gz"`,
		},
		{
			name: "the platform map",
			from: `const osName = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform]`,
			to:   `const osName = { darwin: "macos", linux: "linux", win32: "windows" }[process.platform]`,
		},
		{
			name: "the architecture map",
			from: `const arch = { x64: "amd64", arm64: "arm64" }[process.arch]`,
			to:   `const arch = { x64: "x86_64", arm64: "aarch64" }[process.arch]`,
		},
		{
			name: "the checksums download",
			from: "get(`${base}/checksums.txt`)",
			to:   "get(`${base}/SHA256SUMS`)",
		},
	} {
		src := string(realInstallJS(t))
		if !strings.Contains(src, tc.from) {
			t.Errorf("%s: npm/install.js no longer contains %q, so this case exercises nothing", tc.name, tc.from)
			continue
		}
		mutated := strings.Replace(src, tc.from, tc.to, 1)
		if _, err := InstallerAssetNames([]byte(mutated), mustParseTag(t, "v0.2.8")); err == nil {
			t.Errorf("%s: install.js's rule was rewritten and the restatement kept answering as if it had not", tc.name)
		}
	}
}
