package selfupdate

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLifecycleLockContendsWithinOneProcess(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "dropin-miner")
	release, err := AcquireLifecycleLock(executable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLifecycleLock(executable); !errors.Is(err, ErrLifecycleBusy) {
		t.Fatalf("second acquisition = %v", err)
	}
	release()
	releaseAgain, err := AcquireLifecycleLock(executable)
	if err != nil {
		t.Fatalf("acquisition after release: %v", err)
	}
	releaseAgain()
}
