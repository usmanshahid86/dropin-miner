package selfupdate

import (
	"errors"
	"fmt"
)

var ErrLifecycleBusy = errors.New("another install, upgrade, rollback or uninstall operation is in progress")

// AcquireLifecycleLock makes one deterministic, non-blocking attempt. The
// empty sibling lockfile is permanent: deleting it would race a process that
// already holds a descriptor on the old inode.
func AcquireLifecycleLock(executable string) (func(), error) {
	path := executable + ".update.lock"
	f, held, err := tryLockLifecycleFile(path)
	if err != nil {
		return nil, fmt.Errorf("open lifecycle lock: %w", err)
	}
	if !held {
		return nil, ErrLifecycleBusy
	}
	return func() { _ = unlockLifecycleFile(f) }, nil
}
