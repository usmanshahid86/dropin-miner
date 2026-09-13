package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Git is the thin adapter over the one external program this tool needs.
//
// Shelling out rather than linking a git library is deliberate: every
// question asked here (what kind of object is this tag, what commit does
// it point at, is that commit reachable from canonical main) has an
// exact, well-known git answer, and the tests drive real repositories
// built by real git rather than a model of one. A release gate that
// agreed with a library but disagreed with git would be worse than no
// gate.
type Git struct{ Dir string }

func (g Git) run(args ...string) (string, error) {
	// #nosec G204 -- a fixed binary and argv, never a shell; the tag reaches
	// here only after ParseReleaseTag has restricted it to vX.Y.Z.
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// TagObjectType is "tag" for an annotated tag and "commit" for a
// lightweight one — the only way to tell them apart, since both resolve
// to the same commit.
func (g Git) TagObjectType(tag string) (string, error) {
	return g.run("cat-file", "-t", "refs/tags/"+tag)
}

// TagCommit peels a tag to the commit it releases. ^{commit} is
// deliberate rather than ^{}: it dereferences an annotated tag and is a
// no-op on a lightweight one, so both kinds answer the same question.
func (g Git) TagCommit(tag string) (string, error) {
	return g.run("rev-parse", "refs/tags/"+tag+"^{commit}")
}

// ResolveRef is the commit a ref names.
func (g Git) ResolveRef(ref string) (string, error) {
	return g.run("rev-parse", ref+"^{commit}")
}

// FileAtRev reads a file as of a revision, without touching the
// worktree. Every file this tool judges a release by is read this way:
// "the tag's npm/package.json" must mean the bytes the tag actually
// carries, not whatever happens to be checked out beside it.
func (g Git) FileAtRev(rev, path string) ([]byte, error) {
	// #nosec G204 -- fixed binary and argv; rev is a validated tag or a
	// literal ref this tool chose, path is a compile-time constant.
	cmd := exec.Command("git", "show", rev+":"+path)
	cmd.Dir = g.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w: %s", rev, path, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// IsAncestor reports whether ancestor is reachable from descendant.
//
// `git merge-base --is-ancestor` answers by exit code — 0 yes, 1 no —
// and anything else is a real error. Collapsing "no" into "error" would
// turn a security decision into an infrastructure complaint, so the two
// are separated here.
func (g Git) IsAncestor(ancestor, descendant string) (bool, error) {
	// #nosec G204 -- fixed binary and argv; both arguments are commit SHAs
	// this tool resolved through git itself.
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = g.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w: %s",
		ancestor, descendant, err, strings.TrimSpace(stderr.String()))
}

// CheckAnnotatedTag is the convention docs/RELEASING.md starts at
// v0.2.8: a release tag records who cut it and when.
//
// A lightweight tag is a bare pointer, so that record survives only in
// the push event, which expires. Older tags are mixed and are left
// exactly as they are — retagging a published release would move refs
// that install.sh, install.ps1, npm/install.js and a published
// checksums.txt already resolve against. This applies to the tag being
// released now, which is the only one anybody can still choose.
func CheckAnnotatedTag(objectType, tag string) error {
	switch objectType {
	case "tag":
		return nil
	case "commit":
		return fmt.Errorf("%s is a lightweight tag; releases from v0.2.8 on are annotated.\n"+
			"  Delete it and re-cut: git tag -a %s -m %q <commit> && git push upstream %s\n"+
			"  (If you believe the tag was created annotated, check that the checkout preserved it: "+
			"`git ls-remote --tags` shows a ^{} dereference line for an annotated tag.)",
			tag, tag, tag, tag)
	default:
		return fmt.Errorf("refs/tags/%s is a %q object, which is neither a tag nor a commit", tag, objectType)
	}
}
