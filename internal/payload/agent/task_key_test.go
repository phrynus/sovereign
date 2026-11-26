//go:build linux || windows || darwin

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"testing"
)

func TestApplyTaskKeyEncryptionRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	result := TaskResult{TaskID: 7, Type: "shell", Output: "secret output", Encoding: "base64"}
	if err := applyTaskKeyEncryption(base64.StdEncoding.EncodeToString(key), &result); err != nil {
		t.Fatal(err)
	}
	if !result.EncryptedWithTaskKey || result.Encoding != "" || result.Output == "secret output" {
		t.Fatalf("result was not sealed: %+v", result)
	}
	blob, err := base64.StdEncoding.DecodeString(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil || string(plain) != "secret output" {
		t.Fatalf("sealed result did not round trip: plain=%q err=%v", plain, err)
	}
}

func TestApplyTaskKeyEncryptionRejectsInvalidKeyWithoutMutation(t *testing.T) {
	result := TaskResult{TaskID: 8, Type: "shell", Output: "must remain untouched"}
	before := result
	if err := applyTaskKeyEncryption("not-base64", &result); err == nil {
		t.Fatal("invalid key was accepted")
	}
	if result != before {
		t.Fatalf("failed seal mutated result: got %+v want %+v", result, before)
	}
}
