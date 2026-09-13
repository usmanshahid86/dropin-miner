package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The preflight tests build a repository that looks like this one at a
// release — the real .goreleaser.yaml and the real npm/install.js, so
// the naming-contract coupling is exercised against the contract that
// actually ships — and then break it one way at a time. Each failing
// case is a release somebody could plausibly cut by accident, and the
// point of the job is that none of them reach goreleaser.

type releaseRepo struct {
	t   *testing.T
	dir string
}

func copyFromRepo(t *testing.T, dir, path string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", path)) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, path, string(b))
}

// newReleaseRepo is a repository whose main branch carries a coherent
// release of the given version, with nothing tagged yet.
func newReleaseRepo(t *testing.T, version string) *releaseRepo {
	t.Helper()
	dir := newRepo(t)
	copyFromRepo(t, dir, ".goreleaser.yaml")
	copyFromRepo(t, dir, "npm/install.js")
	write(t, dir, "npm/package.json", `{"name":"dropin-miner","version":"`+version+`"}`+"\n")
	write(t, dir, "CHANGELOG.md", "# Changelog\n\n## v"+version+" — 2026-09-20\n\n- something a participant should know\n")
	commit(t, dir, "release "+version)
	return &releaseRepo{t: t, dir: dir}
}

func (r *releaseRepo) annotate(tag string) {
	r.t.Helper()
	git(r.t, r.dir, "tag", "-a", tag, "-m", tag)
}

func (r *releaseRepo) lightweight(tag string) {
	r.t.Helper()
	git(r.t, r.dir, "tag", tag)
}

// preflight runs the subcommand the way the workflow does, with
// $GITHUB_OUTPUT pointed at a file this test can read back.
func (r *releaseRepo) preflight(tag string) (map[string]string, error) {
	r.t.Helper()
	outPath := filepath.Join(r.t.TempDir(), "github_output")
	if err := os.WriteFile(outPath, nil, 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.t.Setenv("GITHUB_OUTPUT", outPath)
	err := runPreflight([]string{
		"-tag", tag, "-repo", r.dir, "-canonical-ref", "refs/heads/main",
	}, io.Discard)
	return readOutputs(r.t, outPath), err
}

func readOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- a path this test created
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if k, v, ok := strings.Cut(s.Text(), "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestPreflightAcceptsAReleaseCutFromCanonicalMain(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	r.annotate("v0.2.8")
	head := git(t, r.dir, "rev-parse", "HEAD")

	outputs, err := r.preflight("v0.2.8")
	if err != nil {
		t.Fatalf("a correct release was refused: %v", err)
	}
	if outputs["version"] != "0.2.8" {
		t.Errorf("version output = %q, want 0.2.8", outputs["version"])
	}
	if outputs["commit"] != head {
		t.Errorf("commit output = %q, want %q", outputs["commit"], head)
	}
	if outputs["canonical_main"] != head {
		t.Errorf("canonical_main output = %q, want %q", outputs["canonical_main"], head)
	}
}

// The publication gate. Write access sufficient to create a v* tag must
// not by itself be enough to publish: the commit being released has to
// be one canonical main already reached.
//
// Without this, a tag pushed at a branch head — an unmerged PR, a
// reverted commit, a fork's history — would build a GitHub Release and
// then an npm package under this project's own name.
func TestPreflightRefusesATagThatIsNotOnCanonicalMain(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	base := git(t, r.dir, "rev-parse", "HEAD")

	git(t, r.dir, "checkout", "-q", "-b", "not-main", base)
	write(t, r.dir, "npm/install.js", string(mustRead(t, filepath.Join("..", "..", "npm", "install.js"))))
	write(t, r.dir, "sneaky.txt", "never reviewed\n")
	offMain := commit(t, r.dir, "a commit that never reached main")
	r.annotate("v0.2.8")
	git(t, r.dir, "checkout", "-q", "main")

	outputs, err := r.preflight("v0.2.8")
	if err == nil {
		t.Fatal("a tag on a commit that is not an ancestor of canonical main was accepted for publication")
	}
	for _, want := range []string{offMain, base, "canonical main"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not report %q, which a human needs to diagnose it: %v", want, err)
		}
	}
	// The diagnostics are still owed even on a refusal, but nothing
	// downstream may act on them.
	if outputs["version"] != "" {
		t.Errorf("a refused preflight still published version=%q for a later job to use", outputs["version"])
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a repository path this test names
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPreflightRefusesALightweightTag(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	r.lightweight("v0.2.8")
	if _, err := r.preflight("v0.2.8"); err == nil {
		t.Fatal("a lightweight tag was accepted; annotated is the convention from v0.2.8 on")
	}
}

// v0.2.1 and v0.2.2, reproduced end to end: the bump landed one commit
// late, the GitHub Release built perfectly, and npm was left stale.
func TestPreflightRefusesAnNpmVersionThatDoesNotMatchTheTag(t *testing.T) {
	r := newReleaseRepo(t, "0.2.7")
	write(t, r.dir, "CHANGELOG.md", "# Changelog\n\n## v0.2.8 — 2026-09-20\n\n- a thing\n")
	commit(t, r.dir, "changelog for 0.2.8, but the bump never landed")
	r.annotate("v0.2.8")

	_, err := r.preflight("v0.2.8")
	if err == nil {
		t.Fatal("the tag said 0.2.8 and npm/package.json said 0.2.7, and the release was allowed to proceed")
	}
	if !strings.Contains(err.Error(), "0.2.7") {
		t.Errorf("the refusal does not name what the manifest actually said: %v", err)
	}
}

func TestPreflightRefusesAReleaseWithNoChangelogEntry(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	write(t, r.dir, "CHANGELOG.md", "# Changelog\n\n## v0.2.7 — 2026-09-13\n\n- the previous release\n")
	commit(t, r.dir, "no entry for the release being cut")
	r.annotate("v0.2.8")

	if _, err := r.preflight("v0.2.8"); err == nil {
		t.Fatal("a release with no CHANGELOG.md entry of its own was accepted")
	}
}

// Every file the preflight judges must come out of the tag, not out of
// whatever is checked out beside it. Both directions are proven: a
// correct tag stays correct when main moves on, and a wrong tag stays
// wrong when main is fixed afterwards.
func TestPreflightJudgesTheTagsBytesAndNotTheWorktrees(t *testing.T) {
	t.Run("main moving on does not make a correct tag wrong", func(t *testing.T) {
		r := newReleaseRepo(t, "0.2.8")
		r.annotate("v0.2.8")
		write(t, r.dir, "npm/package.json", `{"name":"dropin-miner","version":"0.9.9"}`+"\n")
		commit(t, r.dir, "post-release work on main")

		if _, err := r.preflight("v0.2.8"); err != nil {
			t.Fatalf("a correct tag was refused because main had moved on: %v", err)
		}
	})

	t.Run("fixing main afterwards does not make a wrong tag right", func(t *testing.T) {
		r := newReleaseRepo(t, "0.2.7")
		write(t, r.dir, "CHANGELOG.md", "# Changelog\n\n## v0.2.8 — 2026-09-20\n\n- a thing\n")
		commit(t, r.dir, "changelog only")
		r.annotate("v0.2.8")
		write(t, r.dir, "npm/package.json", `{"name":"dropin-miner","version":"0.2.8"}`+"\n")
		commit(t, r.dir, "the bump, one commit too late")

		if _, err := r.preflight("v0.2.8"); err == nil {
			t.Fatal("a tag whose own commit carried the wrong npm version was accepted " +
				"because a later commit on main fixed it; the tag is what gets published")
		}
	})
}

func TestPreflightRefusesAMalformedTagBeforeTouchingGit(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	r.annotate("v0.2.8")
	for _, tag := range []string{"v0.2.8-rc1", "0.2.8", "release-0.2.8"} {
		if _, err := r.preflight(tag); err == nil {
			t.Errorf("%s was accepted as a release tag", tag)
		}
	}
}

// -canonical-ref is required rather than defaulted, because every
// plausible default is wrong: the checkout branch is the tag itself on a
// tag push, and a local ref may be whatever a previous fetch left.
func TestPreflightRequiresCanonicalMainToBeNamedExplicitly(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	r.annotate("v0.2.8")
	t.Setenv("GITHUB_OUTPUT", "")
	if err := runPreflight([]string{"-tag", "v0.2.8", "-repo", r.dir}, io.Discard); err == nil {
		t.Fatal("preflight ran with no canonical main to compare against")
	}
}
