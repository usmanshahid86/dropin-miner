//go:build !windows

package selfupdate

import "os"

func platformRenameReplace(from, to string) error { return os.Rename(from, to) }
func platformRenameNew(from, to string) error     { return os.Rename(from, to) }
