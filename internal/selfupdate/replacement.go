package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/twilight-project/dropin-miner/pkg/fsx"
)

type ReplacementStrategy string

const (
	ReplacePOSIX   ReplacementStrategy = "posix"
	ReplaceWindows ReplacementStrategy = "windows"
)

// ReplacementError makes the externally important state explicit.
type ReplacementError struct {
	Stage             string
	CanonicalChanged  bool
	PreviousChanged   bool
	RecoveryAttempted bool
	RecoveryFailed    bool
	Err               error
}

func (e *ReplacementError) Error() string {
	return fmt.Sprintf("selfupdate: replacement %s: %v", e.Stage, e.Err)
}

func (e *ReplacementError) Unwrap() error { return e.Err }

type replacementOps struct {
	snapshot      func(string) (string, error)
	reserve       func(string) (string, error)
	renameReplace func(string, string) error
	renameNew     func(string, string) error
	syncDir       func(string) error
	remove        func(string) error
}

func defaultReplacementOps() replacementOps {
	return replacementOps{
		snapshot:      durableSnapshot,
		reserve:       reservePath,
		renameReplace: platformRenameReplace,
		renameNew:     platformRenameNew,
		syncDir: func(dir string) error {
			err := fsx.SyncDirectory(dir)
			if errors.Is(err, fsx.ErrDirectorySyncUnsupported) {
				return nil
			}
			return err
		},
		remove: os.Remove,
	}
}

// Replace installs candidate and preserves the displaced executable at
// executable+.previous. candidate must already be on the same filesystem.
func Replace(executable, candidate string, strategy ReplacementStrategy) error {
	return replaceWithOps(executable, candidate, strategy, defaultReplacementOps())
}

func replaceWithOps(executable, candidate string, strategy ReplacementStrategy, ops replacementOps) error {
	if filepath.Dir(executable) != filepath.Dir(candidate) {
		return &ReplacementError{Stage: "precondition", Err: errors.New("candidate is not beside executable")}
	}
	switch strategy {
	case ReplacePOSIX:
		return replacePOSIX(executable, candidate, ops)
	case ReplaceWindows:
		return replaceWindows(executable, candidate, ops)
	default:
		return &ReplacementError{Stage: "precondition", Err: fmt.Errorf("unknown strategy %q", strategy)}
	}
}

func replacePOSIX(executable, candidate string, ops replacementOps) error {
	dir := filepath.Dir(executable)
	previous := executable + ".previous"
	snapshot, err := ops.snapshot(executable)
	if err != nil {
		return &ReplacementError{Stage: "preserve current", Err: err}
	}
	removeSnapshot := true
	defer func() {
		if removeSnapshot {
			_ = ops.remove(snapshot)
		}
	}()
	if err := ops.renameReplace(candidate, executable); err != nil {
		return &ReplacementError{Stage: "install candidate", Err: err}
	}
	// From this point the snapshot is recovery evidence. If recovery fails,
	// never let best-effort cleanup erase the only preserved old executable.
	removeSnapshot = false
	if err := ops.syncDir(dir); err != nil {
		recoveryErr := ops.renameReplace(snapshot, executable)
		if recoveryErr == nil {
			recoveryErr = ops.syncDir(dir)
		}
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("recovery using preserved executable %s: %w", snapshot, recoveryErr)
		}
		return &ReplacementError{Stage: "sync installed candidate", CanonicalChanged: recoveryErr != nil,
			RecoveryAttempted: true, RecoveryFailed: recoveryErr != nil, Err: errors.Join(err, recoveryErr)}
	}
	if err := ops.renameReplace(snapshot, previous); err != nil {
		recoveryErr := ops.renameReplace(snapshot, executable)
		if recoveryErr == nil {
			recoveryErr = ops.syncDir(dir)
		}
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("recovery using preserved executable %s: %w", snapshot, recoveryErr)
		}
		return &ReplacementError{Stage: "commit previous", CanonicalChanged: recoveryErr != nil,
			RecoveryAttempted: true, RecoveryFailed: recoveryErr != nil, Err: errors.Join(err, recoveryErr)}
	}
	removeSnapshot = false
	if err := ops.syncDir(dir); err != nil {
		return &ReplacementError{Stage: "sync completed transaction", CanonicalChanged: true, PreviousChanged: true, Err: err}
	}
	return nil
}

func replaceWindows(executable, candidate string, ops replacementOps) error {
	dir := filepath.Dir(executable)
	previous := executable + ".previous"
	displaced, err := ops.reserve(dir)
	if err != nil {
		return &ReplacementError{Stage: "reserve displaced path", Err: err}
	}
	removeDisplaced := true
	defer func() {
		if removeDisplaced {
			_ = ops.remove(displaced)
		}
	}()
	if err := ops.renameNew(executable, displaced); err != nil {
		return &ReplacementError{Stage: "move running executable", Err: err}
	}
	// D is the only old executable after the first move. Preserve it on every
	// later error; on success it has been consumed into .previous.
	removeDisplaced = false
	if err := ops.renameNew(candidate, executable); err != nil {
		recoveryErr := ops.renameNew(displaced, executable)
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("recovery using displaced executable %s: %w", displaced, recoveryErr)
		}
		return &ReplacementError{Stage: "install candidate", CanonicalChanged: recoveryErr != nil,
			RecoveryAttempted: true, RecoveryFailed: recoveryErr != nil, Err: errors.Join(err, recoveryErr)}
	}
	if err := ops.renameReplace(displaced, previous); err != nil {
		// Put the verified candidate back at its staging name before restoring
		// the old executable. The prior .previous remains untouched.
		recoveryErr := ops.renameNew(executable, candidate)
		if recoveryErr == nil {
			recoveryErr = ops.renameNew(displaced, executable)
		}
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("recovery using displaced executable %s: %w", displaced, recoveryErr)
		}
		return &ReplacementError{Stage: "commit previous", CanonicalChanged: recoveryErr != nil,
			RecoveryAttempted: true, RecoveryFailed: recoveryErr != nil, Err: errors.Join(err, recoveryErr)}
	}
	return nil
}

func durableSnapshot(source string) (string, error) {
	in, err := os.Open(source) // #nosec G304 -- current executable path
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxExecutableBytes {
		return "", fmt.Errorf("current executable size %d is outside 1..%d bytes", info.Size(), MaxExecutableBytes)
	}
	out, err := os.CreateTemp(filepath.Dir(source), ".dropin-miner.previous-*")
	if err != nil {
		return "", err
	}
	name := out.Name()
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return "", err
	}
	if n, err := io.Copy(out, io.LimitReader(in, MaxExecutableBytes+1)); err != nil || n != info.Size() {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func reservePath(dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".dropin-miner.displaced-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return name, nil
}
