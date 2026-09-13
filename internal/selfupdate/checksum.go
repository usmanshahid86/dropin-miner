package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SHA256Verifier is today's verifier. The interface around it is deliberately
// wider than its two assets so signatures/certificates can be added later.
type SHA256Verifier struct{}

func (SHA256Verifier) RequiredAssets(_ ReleaseInfo, target Artifact) ([]AssetRequirement, error) {
	return []AssetRequirement{
		{Name: target.ArchiveName, MaxBytes: MaxArchiveBytes},
		{Name: ChecksumAssetName, MaxBytes: MaxChecksumBytes},
	}, nil
}

func (SHA256Verifier) Verify(ctx context.Context, assets map[string][]byte, target Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	archive, ok := assets[target.ArchiveName]
	if !ok {
		return fmt.Errorf("verifier did not receive %q", target.ArchiveName)
	}
	sums, ok := assets[ChecksumAssetName]
	if !ok {
		return fmt.Errorf("verifier did not receive %q", ChecksumAssetName)
	}
	expected, err := checksumFor(sums, target.ArchiveName)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(archive)
	if actual != expected {
		return fmt.Errorf("SHA-256 mismatch for %q", target.ArchiveName)
	}
	return nil
}

func checksumFor(raw []byte, target string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	entries := make(map[string][sha256.Size]byte)
	for lineNumber, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 || !safeAssetName(fields[1]) {
			return zero, fmt.Errorf("malformed checksum entry on line %d", lineNumber+1)
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil {
			return zero, fmt.Errorf("malformed SHA-256 on line %d", lineNumber+1)
		}
		var sum [sha256.Size]byte
		copy(sum[:], decoded)
		if _, duplicate := entries[fields[1]]; duplicate {
			return zero, fmt.Errorf("duplicate checksum entry for %q", fields[1])
		}
		entries[fields[1]] = sum
	}
	want, ok := entries[target]
	if !ok {
		return zero, fmt.Errorf("checksums.txt has no entry for %q", target)
	}
	return want, nil
}
