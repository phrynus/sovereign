package protocol

import "testing"

func TestPersistenceMethodsAreUniqueAndLookupWorks(t *testing.T) {
	seen := make(map[string]bool, len(PersistenceMethods))
	for _, method := range PersistenceMethods {
		name := string(method)
		if seen[name] {
			t.Fatalf("duplicate persistence method %q", name)
		}
		seen[name] = true
		if !IsPersistenceMethod(name) {
			t.Fatalf("declared method %q does not look itself up", name)
		}
	}
	if len(PersistenceMethods) < 20 {
		t.Fatalf("only %d methods registered; expected the Windows/macOS/Linux union", len(PersistenceMethods))
	}
	for _, unknown := range []string{"", "Nope", "registry ", "certutil"} {
		if IsPersistenceMethod(unknown) {
			t.Errorf("unknown method %q accepted", unknown)
		}
	}
}
