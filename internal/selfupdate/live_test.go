package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestLiveReleaseVerification performs no replacement. It is opt-in because
// ordinary tests must not depend on GitHub availability:
//
//	DROPIN_MINER_LIVE_RELEASE=1 go test ./internal/selfupdate -run TestLiveReleaseVerification -count=1
func TestLiveReleaseVerification(t *testing.T) {
	if os.Getenv("DROPIN_MINER_LIVE_RELEASE") != "1" {
		t.Skip("set DROPIN_MINER_LIVE_RELEASE=1 for public release verification")
	}
	v, err := ParseVersion("0.2.8")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source := NewHTTPSource(nil)
	release, err := source.Release(ctx, &v)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ArtifactFor(v, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	verifier := SHA256Verifier{}
	requirements, err := verifier.RequiredAssets(release, target)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := source.DownloadAssets(ctx, release, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(ctx, assets, target); err != nil {
		t.Fatal(err)
	}
	binary, err := ArchiveExecutable(assets[target.ArchiveName], target)
	if err != nil {
		t.Fatal(err)
	}
	placeholder := filepath.Join(t.TempDir(), target.ExecutableName)
	candidate, err := StageCandidate(placeholder, binary)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(candidate)
	if err := ValidateCandidate(ctx, ExecRunner{}, candidate, v); err != nil {
		t.Fatal(err)
	}
}
