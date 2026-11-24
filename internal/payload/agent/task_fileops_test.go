//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sha256HexForTest(t *testing.T, data []byte) string {
	t.Helper()
	return sha256Hex(data)
}

func putTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func TestFileWriteNonemptyAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	original := []byte("original text\n")
	putTestFile(t, path, original, 0640)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := []byte("first edit\n")
	res := TaskResult{}
	handleWrite(Task{
		Type:           "write",
		Command:        path,
		ExpectedSHA256: sha256HexForTest(t, original),
		Data:           base64.StdEncoding.EncodeToString(updated),
	}, &res)
	if res.Error != "" {
		t.Fatalf("write failed: %s", res.Error)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(updated) {
		t.Fatalf("content = %q, want %q", got, updated)
	}
	if res.SHA256 != sha256HexForTest(t, updated) {
		t.Fatalf("result sha256 = %q, want %q", res.SHA256, sha256HexForTest(t, updated))
	}
	if res.Mtime <= 0 {
		t.Fatalf("result mtime = %d, want positive Unix seconds", res.Mtime)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode = %v, want %v", after.Mode().Perm(), before.Mode().Perm())
	}

	// Empty content is a valid edit of an existing file.
	empty := TaskResult{}
	handleWrite(Task{
		Type:           "write",
		Command:        path,
		ExpectedSHA256: sha256HexForTest(t, updated),
		Data:           "",
	}, &empty)
	if empty.Error != "" {
		t.Fatalf("empty write failed: %s", empty.Error)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty write left %d bytes", len(got))
	}
	if empty.SHA256 != sha256HexForTest(t, nil) {
		t.Fatalf("empty write sha256 = %q", empty.SHA256)
	}
}

func TestFileWriteHashMismatchAndTempCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.txt")
	original := []byte("original\n")
	putTestFile(t, path, original, 0600)

	res := TaskResult{}
	handleWrite(Task{
		Type:           "write",
		Command:        path,
		ExpectedSHA256: strings.Repeat("0", 64),
		Data:           base64.StdEncoding.EncodeToString([]byte("new\n")),
	}, &res)
	if res.Error != "file_conflict" {
		t.Fatalf("stale baseline error = %q, want file_conflict", res.Error)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("stale edit changed file to %q", got)
	}

	// Mutate the target after the temp file is written: the pre-rename hash
	// must reject the edit and remove the temp file without replacing it.
	previousHook := fileWritePreCommitHook
	fileWritePreCommitHook = func() {
		_ = os.WriteFile(path, []byte("external edit\n"), 0600)
	}
	t.Cleanup(func() { fileWritePreCommitHook = previousHook })
	raced := TaskResult{}
	handleWrite(Task{
		Type:           "write",
		Command:        path,
		ExpectedSHA256: sha256HexForTest(t, original),
		Data:           base64.StdEncoding.EncodeToString([]byte("local edit\n")),
	}, &raced)
	if raced.Error != "file_conflict" {
		t.Fatalf("raced write error = %q, want file_conflict", raced.Error)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "external edit\n" {
		t.Fatalf("raced write replaced target with %q", got)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".*.fc2-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}

	missing := TaskResult{}
	handleWrite(Task{
		Type:           "write",
		Command:        filepath.Join(dir, "absent.txt"),
		ExpectedSHA256: sha256HexForTest(t, nil),
	}, &missing)
	if missing.Error == "" || !strings.Contains(missing.Error, "does not exist") {
		t.Fatalf("missing file error = %q", missing.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent.txt")); !os.IsNotExist(err) {
		t.Fatal("editor created a missing file")
	}
}

func TestFileTransferExplicitDirection(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	payload := []byte("0123456789abcdef")
	putTestFile(t, source, payload, 0644)
	transferID := uint(7)

	// pull: explicit direction controls the read even though task.Data is set.
	pull := TaskResult{}
	handleDownload(Task{
		ID:         100,
		Type:       "download",
		TransferID: &transferID,
		Direction:  "pull",
		Path:       source,
		Offset:     2,
		Size:       5,
		Data:       "ignored",
	}, &pull)
	if pull.Error != "" {
		t.Fatalf("pull failed: %s", pull.Error)
	}
	wantChunk := payload[2:7]
	decoded, err := base64.StdEncoding.DecodeString(pull.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(wantChunk) {
		t.Fatalf("pull chunk = %q, want %q", decoded, wantChunk)
	}
	if pull.SHA256 != sha256HexForTest(t, wantChunk) {
		t.Fatalf("pull sha256 = %q, want %q", pull.SHA256, sha256HexForTest(t, wantChunk))
	}
	if pull.Final {
		t.Fatal("short pull chunk marked final")
	}
	wantMAC := hex.EncodeToString(fileChunkMAC(fileChainKey(), make([]byte, 32), wantChunk))
	if pull.MAC != wantMAC {
		t.Fatalf("pull MAC = %q, want %q", pull.MAC, wantMAC)
	}

	eof := TaskResult{}
	handleDownload(Task{
		ID:         100,
		Type:       "download",
		TransferID: &transferID,
		Direction:  "pull",
		Path:       source,
		Offset:     int64(len(payload)),
		Size:       5,
	}, &eof)
	if eof.Error != "" || !eof.Final {
		t.Fatalf("EOF pull: error=%q final=%v", eof.Error, eof.Final)
	}

	// push: explicit direction writes the signed chunk on an upload task.
	dest := filepath.Join(dir, "dest.bin")
	chunk := []byte("PUSH")
	zeroMAC := hex.EncodeToString(make([]byte, 32))
	chunkMAC := hex.EncodeToString(fileChunkMAC(fileChainKey(), make([]byte, 32), chunk))
	push := TaskResult{}
	handleUpload(Task{
		ID:         101,
		Type:       "upload",
		TransferID: &transferID,
		Direction:  "push",
		Path:       dest,
		Offset:     0,
		Data:       base64.StdEncoding.EncodeToString(chunk),
		PrevMAC:    zeroMAC,
		MAC:        chunkMAC,
		Final:      true,
	}, &push)
	if push.Error != "" {
		t.Fatalf("push failed: %s", push.Error)
	}
	written, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(chunk) {
		t.Fatalf("pushed file = %q, want %q", written, chunk)
	}
	if !push.Final || push.SHA256 != sha256HexForTest(t, chunk) || push.MAC != chunkMAC {
		t.Fatalf("push result: final=%v sha=%q mac=%q", push.Final, push.SHA256, push.MAC)
	}
	if push.Output != base64.StdEncoding.EncodeToString(chunk) || push.Size != int64(len(chunk)) {
		t.Fatalf("push output = %q size=%d; server rebuilds digest from echoed bytes", push.Output, push.Size)
	}

	// A tampered signature must never touch the destination file.
	bad := TaskResult{}
	handleUpload(Task{
		ID:         101,
		Type:       "upload",
		TransferID: &transferID,
		Direction:  "push",
		Path:       dest,
		Offset:     0,
		Data:       base64.StdEncoding.EncodeToString([]byte("BAD!")),
		PrevMAC:    zeroMAC,
		MAC:        chunkMAC,
	}, &bad)
	if bad.Error == "" || !strings.Contains(bad.Error, "HMAC mismatch") {
		t.Fatalf("tampered push error = %q", bad.Error)
	}
	written, _ = os.ReadFile(dest)
	if string(written) != string(chunk) {
		t.Fatalf("tampered push changed file to %q", written)
	}

	// Missing/unknown direction is rejected instead of inferred from Data.
	unknown := TaskResult{}
	handleDownload(Task{
		ID:         100,
		Type:       "download",
		TransferID: &transferID,
		Path:       source,
		Data:       base64.StdEncoding.EncodeToString([]byte("x")),
	}, &unknown)
	if unknown.Error == "" || !strings.Contains(unknown.Error, "unsupported transfer direction") {
		t.Fatalf("unknown direction error = %q", unknown.Error)
	}
}

func TestReadHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	content := []byte("read baseline\n")
	putTestFile(t, path, content, 0644)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res := TaskResult{}
	handleRead(Task{Type: "read", Command: path}, &res)
	if res.Error != "" {
		t.Fatalf("read failed: %s", res.Error)
	}
	if res.Output != base64.StdEncoding.EncodeToString(content) || res.Encoding != "base64" {
		t.Fatalf("read output = %q (%q)", res.Output, res.Encoding)
	}
	if res.SHA256 != sha256HexForTest(t, content) {
		t.Fatalf("read sha256 = %q, want %q", res.SHA256, sha256HexForTest(t, content))
	}
	if res.Mtime != fi.ModTime().Unix() {
		t.Fatalf("read mtime = %d, want %d", res.Mtime, fi.ModTime().Unix())
	}
}
