package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

type archiveLimits struct {
	executable int64
	expanded   uint64
}

// ArchiveExecutable validates the complete archive directory and returns only
// the expected root executable. It never writes a remote member name to disk.
func ArchiveExecutable(archive []byte, target Artifact) ([]byte, error) {
	if int64(len(archive)) > MaxArchiveBytes {
		return nil, fmt.Errorf("archive exceeds %d-byte compressed limit", MaxArchiveBytes)
	}
	limits := archiveLimits{executable: MaxExecutableBytes, expanded: MaxExpandedBytes}
	switch target.ArchiveFormat {
	case "tar.gz":
		return executableFromTarGz(archive, target.ExecutableName, limits)
	case "zip":
		return executableFromZip(archive, target.ExecutableName, limits)
	default:
		return nil, fmt.Errorf("unsupported archive format %q", target.ArchiveFormat)
	}
}

func executableFromTarGz(raw []byte, expected string, limits archiveLimits) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var executable []byte
	var expanded uint64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar archive: %w", err)
		}
		if !safeArchivePath(header.Name) {
			return nil, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if header.Size < 0 || uint64(header.Size) > limits.expanded-expanded {
			return nil, fmt.Errorf("archive expands beyond %d-byte limit", limits.expanded)
		}
		expanded += uint64(header.Size)
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("archive entry %q has unsafe type %d", header.Name, header.Typeflag)
		}
		if header.Name != expected {
			continue
		}
		if executable != nil {
			return nil, fmt.Errorf("archive contains duplicate executable %q", expected)
		}
		if header.Size == 0 || header.Size > limits.executable {
			return nil, fmt.Errorf("executable size %d is outside 1..%d bytes", header.Size, limits.executable)
		}
		executable, err = readBounded(tr, limits.executable)
		if err != nil {
			return nil, fmt.Errorf("read executable %q: %w", expected, err)
		}
		if int64(len(executable)) != header.Size {
			return nil, fmt.Errorf("executable %q is truncated", expected)
		}
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("finish gzip archive: %w", err)
	}
	if executable == nil {
		return nil, fmt.Errorf("archive has no root executable %q", expected)
	}
	return executable, nil
}

func executableFromZip(raw []byte, expected string, limits archiveLimits) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	var executable []byte
	var expanded uint64
	for _, file := range zr.File {
		if !safeArchivePath(file.Name) {
			return nil, fmt.Errorf("unsafe archive path %q", file.Name)
		}
		if file.UncompressedSize64 > limits.expanded-expanded {
			return nil, fmt.Errorf("archive expands beyond %d-byte limit", limits.expanded)
		}
		expanded += file.UncompressedSize64
		mode := file.Mode()
		if mode.IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("archive entry %q has unsafe type %s", file.Name, mode.Type())
		}
		if file.Name != expected {
			continue
		}
		if executable != nil {
			return nil, fmt.Errorf("archive contains duplicate executable %q", expected)
		}
		if limits.executable <= 0 {
			return nil, errors.New("executable size limit is not positive")
		}
		if file.UncompressedSize64 == 0 || file.UncompressedSize64 > uint64(limits.executable) { // #nosec G115 -- positivity checked above; production limit is 64 MiB
			return nil, fmt.Errorf("executable size %d is outside 1..%d bytes", file.UncompressedSize64, limits.executable)
		}
		r, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open executable %q: %w", expected, err)
		}
		executable, err = readBounded(r, limits.executable)
		closeErr := r.Close()
		if err != nil {
			return nil, fmt.Errorf("read executable %q: %w", expected, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close executable %q: %w", expected, closeErr)
		}
		if uint64(len(executable)) != file.UncompressedSize64 {
			return nil, fmt.Errorf("executable %q is truncated", expected)
		}
	}
	if executable == nil {
		return nil, fmt.Errorf("archive has no root executable %q", expected)
	}
	return executable, nil
}

func safeArchivePath(name string) bool {
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	clean := path.Clean(name)
	return clean != "." && !strings.HasPrefix(clean, "../")
}
