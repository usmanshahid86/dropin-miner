//go:build windows

package selfupdate

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryLockLifecycleFile(path string) (*os.File, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return os.NewFile(uintptr(h), path), true, nil
}

func unlockLifecycleFile(f *os.File) error { return f.Close() }
