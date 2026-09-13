//go:build !windows

package selfupdate

import (
	"errors"
	"os"
	"syscall"
)

func tryLockLifecycleFile(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- executable sibling
	if err != nil {
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return f, true, nil
	}
	_ = f.Close()
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, false, nil
	}
	return nil, false, err
}

func unlockLifecycleFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
