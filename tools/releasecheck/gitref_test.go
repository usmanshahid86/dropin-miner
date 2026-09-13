package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The git-facing tests drive real repositories built by real git rather
// than a model of one. A release gate that agreed with a model and
// disagreed with git would be worse than no gate: the model is not what
// decides whether the tag on the server points where it should.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- test-owned argv
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=releasecheck test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=releasecheck test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "commit.gpgsign", "false")
	git(t, dir, "config", "tag.gpgsign", "false")
	return dir
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- a path under this test's own t.TempDir()
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

func TestTagObjectTypeTellsAnnotatedFromLightweight(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "one\n")
	commit(t, dir, "one")
	git(t, dir, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	git(t, dir, "tag", "v1.0.1")

	g := Git{Dir: dir}
	if got, err := g.TagObjectType("v1.0.0"); err != nil || got != "tag" {
		t.Errorf("annotated tag: got %q, %v; want \"tag\"", got, err)
	}
	if got, err := g.TagObjectType("v1.0.1"); err != nil || got != "commit" {
		t.Errorf("lightweight tag: got %q, %v; want \"commit\"", got, err)
	}
}

// Both kinds of tag must peel to the commit they release, or the
// ancestry check would be asking about the wrong object.
func TestTagCommitPeelsBothKindsOfTag(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "one\n")
	head := commit(t, dir, "one")
	git(t, dir, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	git(t, dir, "tag", "v1.0.1")

	g := Git{Dir: dir}
	for _, tag := range []string{"v1.0.0", "v1.0.1"} {
		got, err := g.TagCommit(tag)
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if got != head {
			t.Errorf("%s peeled to %s, want the commit %s", tag, got, head)
		}
	}
}

func TestCheckAnnotatedTag(t *testing.T) {
	if err := CheckAnnotatedTag("tag", "v0.2.8"); err != nil {
		t.Errorf("an annotated tag was rejected: %v", err)
	}
	err := CheckAnnotatedTag("commit", "v0.2.8")
	if err == nil {
		t.Fatal("a lightweight tag was accepted; annotated is the convention from v0.2.8 on")
	}
	if !strings.Contains(err.Error(), "git tag -a") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	if err := CheckAnnotatedTag("blob", "v0.2.8"); err == nil {
		t.Error("a ref that is neither a tag nor a commit was accepted")
	}
}

// merge-base --is-ancestor answers by exit code: 0 yes, 1 no, anything
// else a real error. Collapsing "no" into "error" would turn a security
// decision into an infrastructure complaint, so both answers are proven
// here, and so is the third.
func TestIsAncestorSeparatesNoFromBroken(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "one\n")
	base := commit(t, dir, "one")
	write(t, dir, "a.txt", "two\n")
	onMain := commit(t, dir, "two")

	git(t, dir, "checkout", "-q", "-b", "side", base)
	write(t, dir, "b.txt", "side\n")
	offMain := commit(t, dir, "side")
	git(t, dir, "checkout", "-q", "main")

	g := Git{Dir: dir}
	if ok, err := g.IsAncestor(base, onMain); err != nil || !ok {
		t.Errorf("a commit on main: got %v, %v; want true", ok, err)
	}
	if ok, err := g.IsAncestor(offMain, onMain); err != nil || ok {
		t.Errorf("a commit on a side branch: got %v, %v; want false with no error", ok, err)
	}
	if _, err := g.IsAncestor("0000000000000000000000000000000000000000", onMain); err == nil {
		t.Error("a commit that does not exist was reported as a clean answer rather than an error")
	}
}

// FileAtRev must read the revision, never the worktree. Everything the
// release is judged by depends on this: "npm/package.json at the tag"
// has to mean the bytes the tag carries.
func TestFileAtRevReadsTheRevisionNotTheWorktree(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "npm/package.json", `{"version":"0.2.8"}`)
	commit(t, dir, "tagged state")
	git(t, dir, "tag", "-a", "v0.2.8", "-m", "v0.2.8")
	write(t, dir, "npm/package.json", `{"version":"0.9.9"}`)
	commit(t, dir, "a later, different state")

	got, err := Git{Dir: dir}.FileAtRev("v0.2.8", "npm/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := NPMPackageVersion(got); v != "0.2.8" {
		t.Errorf("read %q from the tag, want 0.2.8: the worktree's later bytes were used instead", v)
	}
}
