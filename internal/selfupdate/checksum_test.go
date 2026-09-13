package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestChecksumVerifierAcceptsExactAsset(t *testing.T) {
	archive := []byte("archive")
	sum := sha256.Sum256(archive)
	target := Artifact{ArchiveName: "dropin-miner_0.3.0_linux_amd64.tar.gz"}
	assets := map[string][]byte{
		target.ArchiveName: archive,
		ChecksumAssetName:  []byte(fmt.Sprintf("%x  %s\n", sum, target.ArchiveName)),
	}
	if err := (SHA256Verifier{}).Verify(context.Background(), assets, target); err != nil {
		t.Fatal(err)
	}
}

func TestChecksumVerifierRejectsDuplicateAndMalformedEntries(t *testing.T) {
	target := Artifact{ArchiveName: "asset.tar.gz"}
	sum := strings.Repeat("0", 64)
	for _, sums := range []string{
		sum + "  asset.tar.gz\n" + sum + "  asset.tar.gz\n",
		"not-a-checksum  asset.tar.gz\n",
		sum + "  ../asset.tar.gz\n",
		sum + "  asset with spaces.tar.gz\n",
	} {
		err := (SHA256Verifier{}).Verify(context.Background(), map[string][]byte{
			target.ArchiveName: nil,
			ChecksumAssetName:  []byte(sums),
		}, target)
		if err == nil {
			t.Errorf("accepted checksums %q", sums)
		}
	}
}

func TestChecksumVerifierRejectsMissingAndMismatch(t *testing.T) {
	target := Artifact{ArchiveName: "asset.tar.gz"}
	for _, sums := range []string{
		strings.Repeat("0", 64) + "  other.tar.gz\n",
		strings.Repeat("0", 64) + "  asset.tar.gzgast\n",
		strings.Repeat("0", 64) + "  asset.tar.gz\n",
	} {
		if err := (SHA256Verifier{}).Verify(context.Background(), map[string][]byte{
			target.ArchiveName: []byte("not zero hash"),
			ChecksumAssetName:  []byte(sums),
		}, target); err == nil {
			t.Errorf("accepted missing/mismatched checksum %q", sums)
		}
	}
}
