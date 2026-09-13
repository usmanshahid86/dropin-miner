package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

type ReleaseSource interface {
	Release(ctx context.Context, requested *Version) (ReleaseInfo, error)
	DownloadAssets(ctx context.Context, release ReleaseInfo, requirements []AssetRequirement) (map[string][]byte, error)
}

type Updater struct {
	Source   ReleaseSource
	Verifier ReleaseVerifier
	Runner   CommandRunner
	GOOS     string
	GOARCH   string
	Strategy ReplacementStrategy
}

type Result struct {
	From       Version
	To         Version
	NoChange   bool
	RolledBack bool
}

func (u Updater) Upgrade(ctx context.Context, executable string, current Version, requested *Version) (Result, error) {
	if u.Source == nil {
		return Result{}, errors.New("selfupdate: no release source")
	}
	verifier := u.Verifier
	if verifier == nil {
		verifier = SHA256Verifier{}
	}
	release, err := u.Source.Release(ctx, requested)
	if err != nil {
		return Result{}, err
	}
	switch release.Version.Compare(current) {
	case 0:
		return Result{From: current, To: current, NoChange: true}, nil
	case -1:
		return Result{}, fmt.Errorf("refusing downgrade from %s to %s; use -rollback for the one preserved version", current, release.Version)
	}
	target, err := ArtifactFor(release.Version, u.GOOS, u.GOARCH)
	if err != nil {
		return Result{}, err
	}
	requirements, err := verifier.RequiredAssets(release, target)
	if err != nil {
		return Result{}, fmt.Errorf("determine verification assets: %w", err)
	}
	assets, err := u.Source.DownloadAssets(ctx, release, requirements)
	if err != nil {
		return Result{}, err
	}
	if err := verifier.Verify(ctx, assets, target); err != nil {
		return Result{}, fmt.Errorf("verify release %s: %w", release.Version.Tag(), err)
	}
	binary, err := ArchiveExecutable(assets[target.ArchiveName], target)
	if err != nil {
		return Result{}, fmt.Errorf("inspect %s: %w", target.ArchiveName, err)
	}
	candidate, err := StageCandidate(executable, binary)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(candidate) }()
	if err := ValidateCandidate(ctx, u.Runner, candidate, release.Version); err != nil {
		return Result{}, err
	}
	if err := Replace(executable, candidate, u.Strategy); err != nil {
		return Result{}, err
	}
	if err := ValidateCandidate(ctx, u.Runner, executable, release.Version); err != nil {
		return Result{}, fmt.Errorf("canonical executable changed but post-install validation failed: %w", err)
	}
	return Result{From: current, To: release.Version}, nil
}

func (u Updater) Rollback(ctx context.Context, executable string, current Version) (Result, error) {
	previous := executable + ".previous"
	previousVersion, err := CandidateVersion(ctx, u.Runner, previous)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("no previous executable is available")
		}
		return Result{}, fmt.Errorf("validate previous executable: %w", err)
	}
	if previousVersion.Compare(current) == 0 {
		return Result{}, fmt.Errorf("previous executable also reports %s; refusing a rollback with no version change", current)
	}
	binary, err := readExecutable(previous)
	if err != nil {
		return Result{}, fmt.Errorf("read previous executable: %w", err)
	}
	candidate, err := StageCandidate(executable, binary)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(candidate) }()
	if err := ValidateCandidate(ctx, u.Runner, candidate, previousVersion); err != nil {
		return Result{}, err
	}
	if err := Replace(executable, candidate, u.Strategy); err != nil {
		return Result{}, err
	}
	if err := ValidateCandidate(ctx, u.Runner, executable, previousVersion); err != nil {
		return Result{}, fmt.Errorf("canonical executable changed but post-rollback validation failed: %w", err)
	}
	return Result{From: current, To: previousVersion, RolledBack: true}, nil
}

func readExecutable(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- fixed sibling of executable
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxExecutableBytes {
		return nil, fmt.Errorf("size %d is outside 1..%d bytes", info.Size(), MaxExecutableBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxExecutableBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, io.ErrUnexpectedEOF
	}
	return data, nil
}
