//go:build linux || darwin
// +build linux darwin

package main

import "os"

// replaceFileAtomically uses same-directory rename(2), which replaces the
// destination atomically on POSIX filesystems.
func replaceFileAtomically(tempPath, destPath string) error {
	return os.Rename(tempPath, destPath)
}
