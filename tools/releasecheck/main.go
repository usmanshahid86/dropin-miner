// Command releasecheck holds the deterministic half of the release
// pipeline: the checks that decide whether a pushed tag may become a
// published release, and whether what was published is what was meant.
//
// It is release tooling and not part of the client. Nothing here is
// compiled into the dropin-miner binary — .goreleaser.yaml builds
// ./cmd/dropin-miner and nothing else — and nothing in cmd/ or pkg/
// imports it.
//
// It does no network I/O and publishes nothing. Everything it judges
// arrives as bytes: the tag's own files come out of git, the release's
// asset list arrives as the JSON `gh release view` prints, the smoke
// test's answer arrives as a captured line. That split is the reason
// these checks are testable at all — orchestration (goreleaser, gh, npm)
// stays in the workflow where it reads as a sequence, and every decision
// with a right answer lives here where a test can drive it, including
// the failing cases a real release will not volunteer.
//
// Exit codes are not a signaling channel. The workflow invokes this
// through `go run`, which collapses any non-zero program exit to 1, so a
// subcommand with something for the workflow to branch on writes it to
// $GITHUB_OUTPUT and exits 0; a non-zero exit means only "stop".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch cmd := os.Args[1]; cmd {
	case "preflight":
		err = runPreflight(os.Args[2:], os.Stdout)
	case "release-state":
		err = runReleaseState(os.Args[2:], os.Stdout)
	case "verify-assets":
		err = runVerifyAssets(os.Args[2:], os.Stdout)
	case "smoke":
		err = runSmoke(os.Args[2:], os.Stdout)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "releasecheck: unknown subcommand %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nreleasecheck: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `releasecheck — the deterministic release gates, for .github/workflows/release.yml

  preflight      -tag vX.Y.Z -repo DIR -canonical-ref REF
                 everything that must hold before anything is built: tag syntax,
                 annotated tag, the tagged commit's ancestry in canonical main,
                 npm/package.json's version at the tag, the changelog heading,
                 and the two naming contracts agreeing.
                 Outputs: version, commit, canonical_main

  release-state  -tag vX.Y.Z -repo DIR -have FILE
                 what the GitHub Release for this tag currently is, so a rerun
                 can resume instead of rebuilding. FILE is the output of
                 "gh release view --json assets", or empty when there is no
                 release. Outputs: state=absent|complete|partial

  verify-assets  -tag vX.Y.Z -repo DIR -have FILE
                 the gate before npm: the release carries exactly the assets
                 .goreleaser.yaml names for this tag, and nothing else.

  smoke          -tag vX.Y.Z -file FILE
                 FILE holds what the installed binary printed for "version".
                 Requires exactly "dropin-miner X.Y.Z".
`)
}

// setOutput hands a value to the next workflow step. Outside Actions
// $GITHUB_OUTPUT is unset and this is a no-op, which is what makes every
// subcommand runnable by hand.
func setOutput(key, value string) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return nil
	}
	// #nosec G304,G703 -- $GITHUB_OUTPUT is the runner's own file, named by the runner.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open $GITHUB_OUTPUT: %w", err)
	}
	defer func() { _ = f.Close() }()
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("refusing to write a multi-line value to $GITHUB_OUTPUT for %q", key)
	}
	if _, err := fmt.Fprintf(f, "%s=%s\n", key, value); err != nil {
		return fmt.Errorf("write $GITHUB_OUTPUT: %w", err)
	}
	return nil
}

func runPreflight(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	tag := fs.String("tag", "", "the release tag that was pushed, vX.Y.Z")
	repo := fs.String("repo", ".", "repository directory")
	canonicalRef := fs.String("canonical-ref", "", "ref naming canonical main, freshly fetched (e.g. refs/remotes/canonical/main)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *canonicalRef == "" {
		return fmt.Errorf("-canonical-ref is required: canonical main must be fetched explicitly, never inferred " +
			"from the checkout branch, a stale local ref, or the tag's own history")
	}

	v, err := ParseReleaseTag(*tag)
	if err != nil {
		return err
	}
	g := Git{Dir: *repo}

	objectType, err := g.TagObjectType(*tag)
	if err != nil {
		return err
	}
	commit, err := g.TagCommit(*tag)
	if err != nil {
		return err
	}
	canonical, err := g.ResolveRef(*canonicalRef)
	if err != nil {
		return fmt.Errorf("resolving canonical main from %s: %w", *canonicalRef, err)
	}
	onMain, err := g.IsAncestor(commit, canonical)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "tag                 %s\n", *tag)
	fmt.Fprintf(out, "tag object          %s\n", objectType)
	fmt.Fprintf(out, "tagged commit       %s\n", commit)
	fmt.Fprintf(out, "canonical main ref  %s\n", *canonicalRef)
	fmt.Fprintf(out, "canonical main      %s\n", canonical)
	fmt.Fprintf(out, "on canonical main   %t\n", onMain)
	fmt.Fprintf(out, "version             %s\n\n", v)

	if err := CheckAnnotatedTag(objectType, *tag); err != nil {
		return err
	}
	if err := CheckCanonicalAncestry(onMain, *tag, commit, canonical); err != nil {
		return err
	}

	manifest, err := g.FileAtRev(*tag, "npm/package.json")
	if err != nil {
		return err
	}
	if err := CheckNPMPackageVersion(manifest, v); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  npm/package.json at the tag says %s\n", v)

	changelog, err := g.FileAtRev(*tag, "CHANGELOG.md")
	if err != nil {
		return err
	}
	if err := CheckReleaseHeading(changelog, v); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  CHANGELOG.md at the tag has a %q heading\n", "## "+v.Tag())

	goreleaserYAML, err := g.FileAtRev(*tag, ".goreleaser.yaml")
	if err != nil {
		return err
	}
	installJS, err := g.FileAtRev(*tag, "npm/install.js")
	if err != nil {
		return err
	}
	if err := CheckNamingContractsAgree(goreleaserYAML, installJS, v); err != nil {
		return err
	}
	contract, err := ParseNamingContract(goreleaserYAML)
	if err != nil {
		return err
	}
	expected, err := contract.ExpectedAssets(v)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  .goreleaser.yaml and npm/install.js name the same %d assets:\n", len(expected))
	for _, name := range expected {
		fmt.Fprintf(out, "      %s\n", name)
	}

	if err := setOutput("version", v.String()); err != nil {
		return err
	}
	if err := setOutput("commit", commit); err != nil {
		return err
	}
	return setOutput("canonical_main", canonical)
}

// CheckCanonicalAncestry is the publication gate that write access to a
// tag namespace must not be able to satisfy on its own.
//
// A syntactically valid vX.Y.Z tag pointing at some commit in the
// repository is not a release. Without this, anyone able to create a v*
// tag could publish a GitHub Release, and then an npm package under the
// project's own name, built from a commit that never passed review. The
// commit being released has to be one canonical main already reached.
//
// This is defense in depth rather than the whole defense, and the
// asymmetry is worth stating: a tag push runs the workflow file as it
// exists at the tagged commit, so a crafted commit can carry a
// release.yml with this check removed. What that crafted workflow cannot
// do is obtain the npm credential, which lives in the `release`
// GitHub Environment and is handed only to a job that declares that
// environment and passes its protection rules. So this check stops the
// honest mistake — a tag on a branch head, a tag on a reverted commit —
// and the environment boundary plus a v* tag-protection ruleset stop the
// dishonest one. See docs/RELEASING.md.
func CheckCanonicalAncestry(onMain bool, tag, commit, canonical string) error {
	if onMain {
		return nil
	}
	return fmt.Errorf("%s points at %s, which is not an ancestor of canonical main (%s).\n"+
		"  A release is cut from a commit that is already on main. Nothing is built and nothing is published.\n"+
		"  If this tag was a mistake: delete it (git push upstream :refs/tags/%s) and tag the merge commit instead",
		tag, commit, canonical, tag)
}

// ghRelease is the shape of `gh release view --json assets`.
type ghRelease struct {
	Assets []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// releaseAssetNames reads the asset list gh printed. An empty input
// means the workflow found no release for this tag, which is a state
// rather than an error — it is what the first run of a release looks
// like.
func releaseAssetNames(b []byte) (names []string, present bool, err error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, false, nil
	}
	var rel ghRelease
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, false, fmt.Errorf("the release JSON does not parse: %w", err)
	}
	for _, a := range rel.Assets {
		names = append(names, a.Name)
	}
	return names, true, nil
}

// expectedAssetsAtTag derives the release's asset names from the tag's
// own .goreleaser.yaml.
func expectedAssetsAtTag(g Git, tag string, v ReleaseVersion) ([]string, error) {
	goreleaserYAML, err := g.FileAtRev(tag, ".goreleaser.yaml")
	if err != nil {
		return nil, err
	}
	contract, err := ParseNamingContract(goreleaserYAML)
	if err != nil {
		return nil, err
	}
	return contract.ExpectedAssets(v)
}

func loadHave(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("-have is required")
	}
	// #nosec G304 -- a path this workflow wrote moments earlier on its own runner.
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// runReleaseState answers the rerun question: is there already a release
// for this tag, and is it the one we meant to make?
//
// Three answers, and the workflow treats each differently. `absent` is a
// first run, so goreleaser builds. `complete` is a rerun after the
// GitHub Release succeeded and something later failed, so goreleaser is
// skipped entirely — rebuilding would either fail on an existing release
// or replace assets a published npm package may already be resolving
// against. `partial` is neither, and it stops: a half-uploaded release
// is a state a human has to look at, not one to paper over by uploading
// the rest of a rebuild.
func runReleaseState(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("release-state", flag.ExitOnError)
	tag := fs.String("tag", "", "the release tag, vX.Y.Z")
	repo := fs.String("repo", ".", "repository directory")
	have := fs.String("have", "", "file holding `gh release view --json assets` output, empty if there is no release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	v, err := ParseReleaseTag(*tag)
	if err != nil {
		return err
	}
	raw, err := loadHave(*have)
	if err != nil {
		return err
	}
	names, present, err := releaseAssetNames(raw)
	if err != nil {
		return err
	}
	if !present {
		fmt.Fprintf(out, "no GitHub Release exists for %s yet\n", *tag)
		return setOutput("state", "absent")
	}
	want, err := expectedAssetsAtTag(Git{Dir: *repo}, *tag, v)
	if err != nil {
		return err
	}
	d := DiffAssets(want, names)
	printDiff(out, d)
	if d.Complete() {
		fmt.Fprintf(out, "\nthe GitHub Release for %s is already complete; goreleaser will be skipped\n", *tag)
		return setOutput("state", "complete")
	}
	if err := setOutput("state", "partial"); err != nil {
		return err
	}
	return fmt.Errorf("a GitHub Release for %s already exists but is not the release this tag describes.\n"+
		"  %v\n"+
		"  This is not resumed automatically: re-running goreleaser over a partial release would replace assets, "+
		"and a published npm package may already be downloading them.\n"+
		"  Look at the release, decide whether to delete and re-run or to repair it by hand, and re-run this workflow",
		*tag, d.Error())
}

// runVerifyAssets is the gate between the GitHub Release and npm.
//
// It is a separate job from the one that made the release, reading the
// release back from the API rather than trusting that goreleaser exited
// zero. The npm wrapper has no binary of its own; it downloads exactly
// these files. Publishing the wrapper before they are all there and
// correct would ship a package that cannot install, and an npm version
// cannot be taken back.
func runVerifyAssets(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify-assets", flag.ExitOnError)
	tag := fs.String("tag", "", "the release tag, vX.Y.Z")
	repo := fs.String("repo", ".", "repository directory")
	have := fs.String("have", "", "file holding `gh release view --json assets` output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	v, err := ParseReleaseTag(*tag)
	if err != nil {
		return err
	}
	raw, err := loadHave(*have)
	if err != nil {
		return err
	}
	names, present, err := releaseAssetNames(raw)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("no GitHub Release exists for %s: nothing may be published to npm", *tag)
	}
	want, err := expectedAssetsAtTag(Git{Dir: *repo}, *tag, v)
	if err != nil {
		return err
	}
	d := DiffAssets(want, names)
	printDiff(out, d)
	if !d.Complete() {
		return d.Error()
	}
	fmt.Fprintf(out, "\nok  the GitHub Release for %s carries exactly the %d expected assets\n", *tag, len(want))
	return nil
}

func printDiff(out io.Writer, d AssetDiff) {
	fmt.Fprintf(out, "expected %d asset(s):\n", len(d.Want))
	for _, n := range d.Want {
		fmt.Fprintf(out, "      %s\n", n)
	}
	fmt.Fprintf(out, "release carries %d asset(s):\n", len(d.Have))
	for _, n := range d.Have {
		fmt.Fprintf(out, "      %s\n", n)
	}
	for _, n := range d.Missing {
		fmt.Fprintf(out, "MISSING     %s\n", n)
	}
	for _, n := range d.Unexpected {
		fmt.Fprintf(out, "UNEXPECTED  %s\n", n)
	}
}

func runSmoke(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	tag := fs.String("tag", "", "the release tag, vX.Y.Z")
	file := fs.String("file", "", "file holding what the installed binary printed for `version`")
	if err := fs.Parse(args); err != nil {
		return err
	}
	v, err := ParseReleaseTag(*tag)
	if err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("-file is required")
	}
	// #nosec G304 -- a path this workflow wrote moments earlier on its own runner.
	got, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read %s: %w", *file, err)
	}
	if err := CheckVersionOutput(got, v); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  the installed binary reports %q\n", "dropin-miner "+v.String())
	return nil
}
