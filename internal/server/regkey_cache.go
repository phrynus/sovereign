package server

import "sync"

// regKeyCache is a bounded memo from a public reg-secret id to its unsealed key.
//
// The v3 registration secret for an agent is immutable once minted (rotation
// rebuilds the whole store and old implants are intentionally invalidated). So
// unsealing the same row on every beacon — and on every chunk of a file-chain
// transfer — is pure repeated work: one DB read plus one AEAD decrypt each time.
// This memo collapses that to one miss per secret id.
//
// Bound: regSecretCacheCap entries, FIFO eviction. The secret id set is bounded
// by the fleet size, so a FIFO cap prevents an attacker who can cause many
// distinct ids to be looked up from pinning unbounded memory. Entries are only
// ever populated for ids that actually exist in the DB and unseal successfully.
const regSecretCacheCap = 256

type regKeyCacheEntry struct {
	id  string
	key []byte
}

type regKeyCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	order   []string // FIFO: oldest first
}

func (c *regKeyCache) get(id string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	// Copy on read: callers must not mutate the memoized key through the slice.
	cp := make([]byte, len(key))
	copy(cp, key)
	return cp, true
}

func (c *regKeyCache) put(id string, key []byte) {
	if id == "" || len(key) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string][]byte, regSecretCacheCap)
	}
	if _, exists := c.entries[id]; exists {
		return // immutable — never overwrite
	}
	c.order = append(c.order, id)
	if len(c.order) > regSecretCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	// Copy: callers must not be able to mutate the memoized key via the slice.
	cp := make([]byte, len(key))
	copy(cp, key)
	c.entries[id] = cp
}

func (c *regKeyCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
	c.order = nil
}
