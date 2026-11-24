//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeFileCAS atomically replaces an EXISTING regular file only when its
// current SHA-256 still matches expectedHex. The original mode is preserved.
// A second hash immediately before the rename narrows (but cannot eliminate)
// the race with an external writer; a change there removes the temp file and
// returns errFileConflict without touching the target.
func writeFileCAS(path, expectedHex string, data []byte) (newHash string, mtime int64, err error) {
	if path == "" {
		return "", 0, fmt.Errorf("path required")
	}
	cleanPath, err := sanitizeWritePath(path)
	if err != nil {
		return "", 0, err
	}
	if len(expectedHex) != 64 {
		return "", 0, fmt.Errorf("expected_sha256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(expectedHex); err != nil {
		return "", 0, fmt.Errorf("expected_sha256 must be 64 hex characters")
	}

	originalHash, fi, err := fileHashStream(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, fmt.Errorf("file does not exist; editor only updates existing files")
		}
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("editor only supports regular files")
	}
	if !strings.EqualFold(originalHash, expectedHex) {
		return "", 0, errFileConflict
	}

	dir := filepath.Dir(cleanPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(cleanPath)+".fc2-*")
	if err != nil {
		return "", 0, err
	}
	tempPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()

	if len(data) > 0 {
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return "", 0, err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(tempPath, fi.Mode().Perm()); err != nil {
		return "", 0, err
	}

	if fileWritePreCommitHook != nil {
		fileWritePreCommitHook()
	}

	currentHash, _, err := fileHashStream(cleanPath)
	if err != nil {
		return "", 0, err
	}
	if !strings.EqualFold(currentHash, expectedHex) {
		return "", 0, errFileConflict
	}

	if err := replaceFileAtomically(tempPath, cleanPath); err != nil {
		return "", 0, err
	}
	keep = true

	if info, err := os.Stat(cleanPath); err == nil {
		mtime = info.ModTime().Unix()
	}
	return sha256Hex(data), mtime, nil
}
