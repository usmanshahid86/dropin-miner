package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeReleaseSource struct {
	release       ReleaseInfo
	assets        map[string][]byte
	downloadCalls int
}

func (s *fakeReleaseSource) Release(context.Context, *Version) (ReleaseInfo, error) {
	return s.release, nil
}

func (s *fakeReleaseSource) DownloadAssets(_ context.Context, _ ReleaseInfo, _ []AssetRequirement) (map[string][]byte, error) {
	s.downloadCalls++
	return s.assets, nil
}

func markerRunner(t *testing.T) CommandRunner {
	t.Helper()
	return runnerFunc(func(_ context.Context, path string, _ []string, _ []string) ([]byte, []byte, error) {
		body, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary path
		if err != nil {
			return nil, nil, err
		}
		version := strings.TrimSpace(string(body))
		return []byte("dropin-miner " + version + "\n"), nil, nil
	})
}

func TestUpgradeRefusesDowngradeBeforeDownloading(t *testing.T) {
	current, _ := ParseVersion("0.3.0")
	older, _ := ParseVersion("0.2.8")
	source := &fakeReleaseSource{release: ReleaseInfo{Version: older}}
	_, err := (Updater{Source: source}).Upgrade(context.Background(), "/tmp/dropin-miner", current, &older)
	if err == nil || !strings.Contains(err.Error(), "refusing downgrade") {
		t.Fatalf("downgrade error = %v", err)
	}
	if source.downloadCalls != 0 {
		t.Fatal("downgrade downloaded assets")
	}
}

func TestUpgradeEqualVersionIsSuccessfulNoOp(t *testing.T) {
	current, _ := ParseVersion("0.3.0")
	source := &fakeReleaseSource{release: ReleaseInfo{Version: current}}
	result, err := (Updater{Source: source}).Upgrade(context.Background(), "/tmp/dropin-miner", current, nil)
	if err != nil || !result.NoChange || source.downloadCalls != 0 {
		t.Fatalf("result = %#v, downloads=%d, err=%v", result, source.downloadCalls, err)
	}
}

func TestUpgradeEndToEndWithInjectedReleaseAndRunner(t *testing.T) {
	current, _ := ParseVersion("0.2.8")
	targetVersion, _ := ParseVersion("0.3.0")
	artifact, _ := ArtifactFor(targetVersion, "linux", "amd64")
	archive := makeTarGz(t, archiveEntry{name: "dropin-miner", body: "0.3.0\n"})
	sum := sha256.Sum256(archive)
	source := &fakeReleaseSource{
		release: ReleaseInfo{Version: targetVersion, Assets: map[string]ReleaseAsset{
			artifact.ArchiveName: {Name: artifact.ArchiveName, Size: int64(len(archive))},
			ChecksumAssetName:    {Name: ChecksumAssetName, Size: 100},
		}},
		assets: map[string][]byte{
			artifact.ArchiveName: archive,
			ChecksumAssetName:    []byte(fmt.Sprintf("%x  %s\n", sum, artifact.ArchiveName)),
		},
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "dropin-miner")
	if err := os.WriteFile(executable, []byte("0.2.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Updater{Source: source, Runner: markerRunner(t), GOOS: "linux", GOARCH: "amd64", Strategy: ReplacePOSIX}).Upgrade(
		context.Background(), executable, current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != current || result.To != targetVersion || fileBody(t, executable) != "0.3.0\n" || fileBody(t, executable+".previous") != "0.2.8\n" {
		t.Fatalf("result=%#v current=%q previous=%q", result, fileBody(t, executable), fileBody(t, executable+".previous"))
	}
}

func TestRollbackValidatesAndSwapsOneLevelWithoutNetwork(t *testing.T) {
	current, _ := ParseVersion("0.3.0")
	dir := t.TempDir()
	executable := filepath.Join(dir, "dropin-miner")
	if err := os.WriteFile(executable, []byte("0.3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable+".previous", []byte("0.2.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Updater{Runner: markerRunner(t), Strategy: ReplacePOSIX}).Rollback(context.Background(), executable, current)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RolledBack || fileBody(t, executable) != "0.2.8\n" || fileBody(t, executable+".previous") != "0.3.0\n" {
		t.Fatalf("result=%#v current=%q previous=%q", result, fileBody(t, executable), fileBody(t, executable+".previous"))
	}
}
