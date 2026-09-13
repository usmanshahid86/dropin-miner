//go:build windows

package selfupdate

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/windows"
)

func platformRenameReplace(from, to string) error {
	return moveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func platformRenameNew(from, to string) error {
	err := moveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return fs.ErrExist
	}
	return err
}

func moveFileEx(from, to string, flags uint32) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, flags)
}
