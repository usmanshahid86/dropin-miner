package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectOwnershipRecognizesNativeAndNPMLayouts(t *testing.T) {
	root := t.TempDir()
	nativeDir := filepath.Join(root, "native", "bin")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(nativeDir, ProjectName)
	if err := os.WriteFile(native, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := DetectOwnership(native); err != nil || got != OwnershipNative {
		t.Fatalf("native ownership = %q, %v", got, err)
	}

	npmRoot := filepath.Join(root, "node_modules", ProjectName)
	npmBin := filepath.Join(npmRoot, "bin")
	if err := os.MkdirAll(npmBin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		filepath.Join(npmRoot, "package.json"):   `{"name":"dropin-miner","version":"0.3.0"}`,
		filepath.Join(npmRoot, "install.js"):     "installer",
		filepath.Join(npmBin, "dropin-miner.js"): "wrapper",
		filepath.Join(npmBin, "dropin-miner"):    "binary",
	} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, err := DetectOwnership(filepath.Join(npmBin, ProjectName)); err != nil || got != OwnershipNPM {
		t.Fatalf("npm ownership = %q, %v", got, err)
	}
}

func TestDetectOwnershipRefusesPartialNPMMarkers(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, ProjectName)
	if err := os.WriteFile(executable, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"dropin-miner"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := DetectOwnership(executable); err == nil || got != OwnershipAmbiguous {
		t.Fatalf("partial ownership = %q, %v", got, err)
	}
}
