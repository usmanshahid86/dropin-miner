package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rerun tests. A release can fail after the GitHub Release exists
// and before npm is published, and the workflow has to be able to
// resume without moving the tag, rebuilding a different commit, or
// inventing a version. What it may do depends entirely on which of
// three states the release is in, so each of the three is driven here.

func ghAssetsJSON(t *testing.T, names ...string) string {
	t.Helper()
	type asset struct {
		Name string `json:"name"`
	}
	payload := struct {
		Assets []asset `json:"assets"`
	}{}
	for _, n := range names {
		payload.Assets = append(payload.Assets, asset{Name: n})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r *releaseRepo) haveFile(content string) string {
	r.t.Helper()
	path := filepath.Join(r.t.TempDir(), "have.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return path
}

func (r *releaseRepo) releaseState(tag, havePath string) (map[string]string, error) {
	r.t.Helper()
	outPath := filepath.Join(r.t.TempDir(), "github_output")
	if err := os.WriteFile(outPath, nil, 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.t.Setenv("GITHUB_OUTPUT", outPath)
	err := runReleaseState([]string{"-tag", tag, "-repo", r.dir, "-have", havePath}, io.Discard)
	return readOutputs(r.t, outPath), err
}

func taggedRepo(t *testing.T) *releaseRepo {
	t.Helper()
	r := newReleaseRepo(t, "0.2.8")
	r.annotate("v0.2.8")
	return r
}

// A first run: nothing exists yet, so goreleaser builds.
func TestReleaseStateReportsAbsentWhenThereIsNoRelease(t *testing.T) {
	r := taggedRepo(t)
	outputs, err := r.releaseState("v0.2.8", r.haveFile(""))
	if err != nil {
		t.Fatalf("no release yet is a state, not a failure: %v", err)
	}
	if outputs["state"] != "absent" {
		t.Errorf("state = %q, want absent", outputs["state"])
	}
}

// The rerun that matters: the GitHub Release succeeded and npm publish
// failed. goreleaser must be skipped, not re-run — re-running it would
// replace assets a published npm package may already be downloading.
func TestReleaseStateReportsCompleteSoARerunSkipsGoreleaser(t *testing.T) {
	r := taggedRepo(t)
	outputs, err := r.releaseState("v0.2.8", r.haveFile(ghAssetsJSON(t, wantAssets028...)))
	if err != nil {
		t.Fatalf("a complete release was reported as a failure: %v", err)
	}
	if outputs["state"] != "complete" {
		t.Errorf("state = %q, want complete", outputs["state"])
	}
}

// The state nothing may resume from on its own. A half-uploaded release
// is not a first run and is not a finished one; papering over it by
// re-running goreleaser would replace whatever did upload.
func TestReleaseStateStopsOnAPartialRelease(t *testing.T) {
	r := taggedRepo(t)
	partial := wantAssets028[:len(wantAssets028)-1]
	outputs, err := r.releaseState("v0.2.8", r.haveFile(ghAssetsJSON(t, partial...)))
	if err == nil {
		t.Fatal("a release missing an asset was resumed automatically")
	}
	if outputs["state"] != "partial" {
		t.Errorf("state = %q, want partial", outputs["state"])
	}
	if !strings.Contains(err.Error(), wantAssets028[len(wantAssets028)-1]) {
		t.Errorf("the refusal does not name the missing asset: %v", err)
	}
}

func TestReleaseStateStopsOnAReleaseCarryingSomethingUnexpected(t *testing.T) {
	r := taggedRepo(t)
	extra := append(append([]string(nil), wantAssets028...), "dropin-miner_0.2.8_linux_amd64.deb")
	outputs, err := r.releaseState("v0.2.8", r.haveFile(ghAssetsJSON(t, extra...)))
	if err == nil {
		t.Fatal("a release carrying an artifact kind this check cannot name was treated as complete")
	}
	if outputs["state"] != "partial" {
		t.Errorf("state = %q, want partial", outputs["state"])
	}
}

// verify-assets is the gate between the GitHub Release and npm. It is a
// separate job reading the release back from the API rather than
// trusting that goreleaser exited zero, because the npm wrapper has no
// binary of its own and an npm version cannot be taken back.
func TestVerifyAssetsGatesNpmOnTheCompleteAssetSet(t *testing.T) {
	r := taggedRepo(t)
	verify := func(content string) error {
		r.t.Setenv("GITHUB_OUTPUT", "")
		return runVerifyAssets([]string{"-tag", "v0.2.8", "-repo", r.dir, "-have", r.haveFile(content)}, io.Discard)
	}

	if err := verify(ghAssetsJSON(t, wantAssets028...)); err != nil {
		t.Fatalf("a complete release was refused: %v", err)
	}
	for _, tc := range []struct {
		name, have string
	}{
		{"one platform's archive never uploaded", ghAssetsJSON(t, wantAssets028[:6]...)},
		{"checksums.txt missing, so install.js cannot verify anything", ghAssetsJSON(t, wantAssets028[1:]...)},
		{"no release at all", ""},
		{"a release with no assets", ghAssetsJSON(t)},
		{"unparseable release JSON", "{not json"},
	} {
		if err := verify(tc.have); err == nil {
			t.Errorf("%s: npm publication was allowed to proceed", tc.name)
		}
	}
}

func TestReleaseStateAndVerifyAssetsRefuseAMalformedTag(t *testing.T) {
	r := taggedRepo(t)
	have := r.haveFile(ghAssetsJSON(t, wantAssets028...))
	r.t.Setenv("GITHUB_OUTPUT", "")
	if err := runReleaseState([]string{"-tag", "latest", "-repo", r.dir, "-have", have}, io.Discard); err == nil {
		t.Error("release-state accepted a ref that is not a release tag")
	}
	if err := runVerifyAssets([]string{"-tag", "latest", "-repo", r.dir, "-have", have}, io.Discard); err == nil {
		t.Error("verify-assets accepted a ref that is not a release tag")
	}
}
