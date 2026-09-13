package selfupdate

import "fmt"

const (
	ProjectName       = "dropin-miner"
	ChecksumAssetName = "checksums.txt"

	// MaxArchiveBytes is deliberately about eight times the largest v0.2.8
	// archive (7.9 MB), leaving growth room without making the bound fictional.
	MaxArchiveBytes    int64  = 64 << 20
	MaxChecksumBytes   int64  = 64 << 10
	MaxExecutableBytes int64  = 64 << 20
	MaxExpandedBytes   uint64 = 128 << 20
)

// Artifact identifies the one release archive and executable for a target.
type Artifact struct {
	GOOS           string
	GOARCH         string
	ArchiveName    string
	ExecutableName string
	ArchiveFormat  string
}

// ArtifactFor implements the checked-in runtime naming contract. The release
// checker has a cross-contract test tying every result to .goreleaser.yaml, so
// a GoReleaser naming change fails CI rather than silently producing 404s.
func ArtifactFor(v Version, goos, goarch string) (Artifact, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return Artifact{}, fmt.Errorf("unsupported upgrade architecture %s", goarch)
	}
	var ext, executable, format string
	switch goos {
	case "linux", "darwin":
		ext, executable, format = ".tar.gz", ProjectName, "tar.gz"
	case "windows":
		ext, executable, format = ".zip", ProjectName+".exe", "zip"
	default:
		return Artifact{}, fmt.Errorf("unsupported upgrade operating system %s", goos)
	}
	return Artifact{
		GOOS:           goos,
		GOARCH:         goarch,
		ArchiveName:    fmt.Sprintf("%s_%s_%s_%s%s", ProjectName, v, goos, goarch, ext),
		ExecutableName: executable,
		ArchiveFormat:  format,
	}, nil
}
