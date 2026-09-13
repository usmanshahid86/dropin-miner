package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io/fs"
	"strings"
	"testing"
)

type archiveEntry struct {
	name     string
	body     string
	typeflag byte
	mode     fs.FileMode
}

func makeTarGz(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.body)), Typeflag: typeflag}
		if typeflag == tar.TypeSymlink || typeflag == tar.TypeLink {
			header.Size, header.Linkname = 0, "target"
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func makeZip(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		} else {
			header.SetMode(0o755)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveExecutableReadsOnlyExpectedRootMember(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		art  Artifact
	}{
		{"tar", makeTarGz(t, archiveEntry{name: "README.md", body: "readme"}, archiveEntry{name: "dropin-miner", body: "binary"}), Artifact{ArchiveFormat: "tar.gz", ExecutableName: "dropin-miner"}},
		{"zip", makeZip(t, archiveEntry{name: "README.md", body: "readme"}, archiveEntry{name: "dropin-miner.exe", body: "binary"}), Artifact{ArchiveFormat: "zip", ExecutableName: "dropin-miner.exe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ArchiveExecutable(tc.raw, tc.art)
			if err != nil || string(got) != "binary" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestArchiveExecutableRejectsTraversalAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		art  Artifact
	}{
		{"tar traversal", makeTarGz(t, archiveEntry{name: "../dropin-miner", body: "x"}), Artifact{ArchiveFormat: "tar.gz", ExecutableName: "dropin-miner"}},
		{"tar symlink", makeTarGz(t, archiveEntry{name: "dropin-miner", typeflag: tar.TypeSymlink}), Artifact{ArchiveFormat: "tar.gz", ExecutableName: "dropin-miner"}},
		{"tar hardlink", makeTarGz(t, archiveEntry{name: "dropin-miner", typeflag: tar.TypeLink}), Artifact{ArchiveFormat: "tar.gz", ExecutableName: "dropin-miner"}},
		{"zip traversal", makeZip(t, archiveEntry{name: "../dropin-miner.exe", body: "x"}), Artifact{ArchiveFormat: "zip", ExecutableName: "dropin-miner.exe"}},
		{"zip symlink", makeZip(t, archiveEntry{name: "dropin-miner.exe", body: "target", mode: fs.ModeSymlink | 0o777}), Artifact{ArchiveFormat: "zip", ExecutableName: "dropin-miner.exe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ArchiveExecutable(tc.raw, tc.art); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestArchiveExecutableRejectsDuplicateBinary(t *testing.T) {
	raw := makeTarGz(t,
		archiveEntry{name: "dropin-miner", body: "one"},
		archiveEntry{name: "dropin-miner", body: "two"},
	)
	if _, err := ArchiveExecutable(raw, Artifact{ArchiveFormat: "tar.gz", ExecutableName: "dropin-miner"}); err == nil {
		t.Fatal("duplicate executable accepted")
	}
}

func TestArchiveExecutableRejectsDecompressionLimit(t *testing.T) {
	raw := makeTarGz(t, archiveEntry{name: "dropin-miner", body: strings.Repeat("x", 17)})
	_, err := executableFromTarGz(raw, "dropin-miner", archiveLimits{executable: 16, expanded: 16})
	if err == nil {
		t.Fatal("oversized expansion accepted")
	}
}
