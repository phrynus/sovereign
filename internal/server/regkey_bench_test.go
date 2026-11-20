package server

import (
	"encoding/hex"
	"testing"

	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/testutil"
)

// BenchmarkRegKeyDerivation measures the two shapes of v3 registration-key
// recovery. The uncached path is a DB read of the reg_secrets row plus an AEAD
// Unseal; it runs on the beacon path (kill-switch enforcement) and per chunk of
// a file-chain transfer. The memoized path is a mutex + map lookup. Two variants:
//   - raw: s.unsealRegSecretByID — the uncached DB read + unseal (baseline)
//   - memo: s.regSecretByID — the bounded secret-id memo
//
// The surrounding Pluck(secret_id) is identical in both and is not measured
// here (the kill-switch caller now supplies the already-loaded SecretID).
// Run: go test ./internal/server/ -run '^$' -bench BenchmarkRegKeyDerivation -benchtime=500x
func BenchmarkRegKeyDerivation(b *testing.B) {
	database := testutil.SetupTestDB(b)
	const master = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	masterBytes, _ := hex.DecodeString(master)
	s := &Server{db: database, regSecrets: crypto.NewRegSecretStore(masterBytes)}

	id, _, _, err := s.ensureV3RegSecret(master, 0)
	if err != nil {
		b.Fatal(err)
	}
	if got := s.unsealRegSecretByID(id); len(got) != 32 {
		b.Fatalf("unseal = %d bytes", len(got))
	}

	b.Run("raw-unseal", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if k := s.unsealRegSecretByID(id); len(k) != 32 {
				b.Fatal("bad key")
			}
		}
	})

	b.Run("memo", func(b *testing.B) {
		s.regKeyCache.put(id, s.unsealRegSecretByID(id)) // prime once
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if k := s.regSecretByID(id); len(k) != 32 {
				b.Fatal("bad key")
			}
		}
	})
}
