//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// applyTaskKeyEncryption seals a task result's Output with the per-task
// AES-256-GCM key issued by the operator. Any key or crypto failure is
// returned to the worker, which drops the output instead of sending plaintext.
func applyTaskKeyEncryption(keyB64 string, res *TaskResult) error {
	if res == nil {
		return fmt.Errorf("nil task result")
	}
	if keyB64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("invalid task key: need 32 raw bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ct := gcm.Seal(nonce, nonce, []byte(res.Output), nil)
	res.Output = base64.StdEncoding.EncodeToString(ct)
	res.EncryptedWithTaskKey = true
	res.Encoding = ""
	return nil
}
