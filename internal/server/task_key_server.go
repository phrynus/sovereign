package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// decryptTaskKeyOutput reverses applyTaskKeyEncryption on the agent: it opens
// the base64 AES-256-GCM ciphertext with the operator-issued per-task key.
func decryptTaskKeyOutput(keyB64, outputB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("invalid task key length %d, want 32", len(raw))
	}
	ct, err := base64.StdEncoding.DecodeString(outputB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ct) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short %d < %d", len(ct), gcm.NonceSize())
	}
	nonce, sealed := ct[:gcm.NonceSize()], ct[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// generateTaskKey returns a fresh base64-encoded 32-byte (AES-256) key for
// per-task result encryption.
func generateTaskKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// decryptTaskResultKey clears an undecryptable result instead of allowing
// ciphertext, or a plaintext fallback, to reach task storage and subscribers.
func decryptTaskResultKey(keyB64 string, r *taskResult) error {
	if r == nil || !r.EncryptedWithTaskKey {
		return nil
	}
	plain, err := decryptTaskKeyOutput(keyB64, r.Output)
	if err != nil {
		r.Output = ""
		r.Error = "per-task result decryption failed"
		r.EncryptedWithTaskKey = false
		return err
	}
	r.Output = plain
	r.EncryptedWithTaskKey = false
	return nil
}
