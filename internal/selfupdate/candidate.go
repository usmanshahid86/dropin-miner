package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/twilight-project/dropin-miner/pkg/fsx"
)

const CandidateTimeout = 5 * time.Second

type CommandRunner interface {
	Run(ctx context.Context, path string, args, env []string) (stdout, stderr []byte, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- verified staged executable, fixed argument
	cmd.Env = env
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// StageCandidate writes a verified archive member beside the installed binary,
// so the eventual rename cannot cross filesystems.
func StageCandidate(executablePath string, binary []byte) (string, error) {
	dir := filepath.Dir(executablePath)
	f, err := os.CreateTemp(dir, ".dropin-miner.candidate-*") // #nosec G304 -- executable-owned directory
	if err != nil {
		return "", fmt.Errorf("stage candidate: %w", err)
	}
	name := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0o755); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("stage candidate permissions: %w", err)
	}
	if n, err := f.Write(binary); err != nil || n != len(binary) {
		_ = f.Close()
		if err == nil {
			err = errors.New("short write")
		}
		return "", fmt.Errorf("stage candidate bytes: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("sync candidate: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close candidate: %w", err)
	}
	if err := fsx.SyncDirectory(dir); err != nil && !errors.Is(err, fsx.ErrDirectorySyncUnsupported) {
		return "", fmt.Errorf("sync candidate directory: %w", err)
	}
	keep = true
	return name, nil
}

func ValidateCandidate(ctx context.Context, runner CommandRunner, path string, expected Version) error {
	got, err := CandidateVersion(ctx, runner, path)
	if err != nil {
		return err
	}
	if got.Compare(expected) != 0 {
		return fmt.Errorf("candidate reports %s, want %s", got, expected)
	}
	return nil
}

// CandidateVersion executes only the candidate's version command and accepts
// only the exact stable release output contract.
func CandidateVersion(ctx context.Context, runner CommandRunner, path string) (Version, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	validationCtx, cancel := context.WithTimeout(ctx, CandidateTimeout)
	defer cancel()
	stdout, stderr, err := runner.Run(validationCtx, path, []string{"version"}, validationEnvironment(os.Environ()))
	if err != nil {
		if errors.Is(validationCtx.Err(), context.DeadlineExceeded) {
			return Version{}, fmt.Errorf("candidate version command timed out (maximum %s)", CandidateTimeout)
		}
		return Version{}, fmt.Errorf("candidate version command failed: %w", err)
	}
	if len(stderr) != 0 {
		return Version{}, fmt.Errorf("candidate version command wrote unexpected stderr %q", stderr)
	}
	line := string(stdout)
	if !strings.HasPrefix(line, "dropin-miner ") || !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		return Version{}, fmt.Errorf("candidate version output %q is not exact", stdout)
	}
	rawVersion := strings.TrimSuffix(strings.TrimPrefix(line, "dropin-miner "), "\n")
	v, err := ParseVersion(rawVersion)
	if err != nil || rawVersion != v.String() {
		return Version{}, fmt.Errorf("candidate version output %q is not a canonical release version", stdout)
	}
	return v, nil
}

func validationEnvironment(source []string) []string {
	allowed := map[string]bool{"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true}
	out := []string{"LANG=C", "LC_ALL=C"}
	if runtime.GOOS == "windows" {
		for _, item := range source {
			key, _, ok := strings.Cut(item, "=")
			if ok && allowed[strings.ToUpper(key)] {
				out = append(out, item)
			}
		}
	}
	return out
}
