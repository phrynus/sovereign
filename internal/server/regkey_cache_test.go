package server

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/r0si/sovereign/internal/db"
)

// TestDeriveRegKeyUsesMemo proves the production derivation path (not just the
// isolated cache) is actually wired to the memo: after the first derivation
// deletes the backing row, a second call still returns the key from cache.
func TestDeriveRegKeyUsesMemo(t *testing.T) {
	s := newRegSecretServer(t, make([]byte, 32))
	id, raw, _, err := s.ensureV3RegSecret("000102030405060708090a0b0c0d0e0f"+"101112131415161718191a1b1c1d1e1f", 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	if err := s.db.Create(&db.Implant{ID: "agent-memo", SecretID: id, TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}

	first := s.deriveRegKey("agent-memo")
	if len(first) != 32 {
		t.Fatalf("first derivation = %d bytes", len(first))
	}
	// Remove the row so an uncached second call would return nil.
	if err := s.db.Where("id = ?", id).Delete(&db.RegSecret{}).Error; err != nil {
		t.Fatal(err)
	}
	second := s.deriveRegKey("agent-memo")
	if !bytes.Equal(first, second) {
		t.Fatalf("second derivation did not use memo: %x vs %x", first, second)
	}
}

func TestRegKeyCachePutGetImmutableAndBounded(t *testing.T) {
	var c regKeyCache
	if _, ok := c.get("missing"); ok {
		t.Fatal("empty cache returned a value")
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	c.put("s1", key)
	got, ok := c.get("s1")
	if !ok || !bytes.Equal(got, key) {
		t.Fatalf("get(s1) = %x, %v", got, ok)
	}

	// Mutating the returned slice must not corrupt the memo.
	got[0] = 0xff
	again, _ := c.get("s1")
	if again[0] != 0x42 {
		t.Fatal("caller mutation leaked into the cached key")
	}

	// Re-putting the same id is a no-op (immutable secret key).
	c.put("s1", bytes.Repeat([]byte{0x11}, 32))
	again, _ = c.get("s1")
	if again[0] != 0x42 {
		t.Fatal("cached key was overwritten")
	}

	// FIFO eviction keeps at most regSecretCacheCap entries.
	for i := 0; i < regSecretCacheCap+10; i++ {
		c.put(fmt.Sprintf("s-%03d", i), []byte{byte(i)})
	}
	if len(c.order) != regSecretCacheCap || len(c.entries) != regSecretCacheCap {
		t.Fatalf("cache not bounded: order=%d entries=%d", len(c.order), len(c.entries))
	}
	if _, ok := c.get("s1"); ok {
		t.Fatal("oldest entry was not evicted")
	}

	c.clear()
	if len(c.entries) != 0 || len(c.order) != 0 {
		t.Fatal("clear did not empty the cache")
	}
}
