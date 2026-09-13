package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type InstallationOwnership string

const (
	OwnershipNative    InstallationOwnership = "native"
	OwnershipNPM       InstallationOwnership = "npm"
	OwnershipAmbiguous InstallationOwnership = "ambiguous"
)

// DetectOwnership recognizes the layout npm/install.js creates today:
// package.json and install.js in the package root, plus wrapper and native
// executable in bin. PR B should replace this isolated heuristic with durable
// installation metadata if it introduces such metadata.
func DetectOwnership(executable string) (InstallationOwnership, string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return OwnershipAmbiguous, "", fmt.Errorf("resolve executable path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return OwnershipAmbiguous, resolved, fmt.Errorf("inspect executable: %w", err)
	}
	binDir := filepath.Dir(resolved)
	if filepath.Base(binDir) != "bin" {
		return OwnershipNative, resolved, nil
	}
	root := filepath.Dir(binDir)
	manifestPath := filepath.Join(root, "package.json")
	installPath := filepath.Join(root, "install.js")
	wrapperPath := filepath.Join(binDir, "dropin-miner.js")
	manifest, manifestErr := os.ReadFile(manifestPath) // #nosec G304 -- derived sibling of the resolved executable
	_, installErr := os.Stat(installPath)
	_, wrapperErr := os.Stat(wrapperPath)
	allMissing := errorsAreNotExist(manifestErr, installErr, wrapperErr)
	if allMissing {
		return OwnershipNative, resolved, nil
	}
	if manifestErr != nil || installErr != nil || wrapperErr != nil {
		return OwnershipAmbiguous, resolved, fmt.Errorf("partial npm-like installation beside %s", resolved)
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil || pkg.Name != ProjectName {
		return OwnershipAmbiguous, resolved, fmt.Errorf("unrecognized package metadata beside %s", resolved)
	}
	return OwnershipNPM, resolved, nil
}

func errorsAreNotExist(errs ...error) bool {
	for _, err := range errs {
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}
