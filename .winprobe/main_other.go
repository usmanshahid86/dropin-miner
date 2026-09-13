//go:build !windows

// Command winprobe is a Windows-only probe; elsewhere it only says so.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "winprobe: Windows only")
	os.Exit(2)
}
