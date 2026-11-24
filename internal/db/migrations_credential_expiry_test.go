package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestCredentialExpiryBackfillLegacySchema covers the demo upgrade path:
// credential_entries existed before subtype/expiry_known, and schema migrations
// run before AutoMigrate adds the newer columns.
func TestCredentialExpiryBackfillLegacySchema(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := database.Exec("CREATE TABLE credential_entries (id INTEGER PRIMARY KEY, expires_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("ALTER TABLE credential_entries ADD COLUMN expiry_known BOOLEAN DEFAULT FALSE").Error; err != nil {
		t.Fatal(err)
	}
	// No subtype column yet: this must be a no-op, not a failed UPDATE.
	if err := backfillCredentialExpiryKnown(database); err != nil {
		t.Fatalf("legacy schema without subtype: %v", err)
	}

	if err := database.Exec("ALTER TABLE credential_entries ADD COLUMN subtype TEXT").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO credential_entries (id, subtype, expires_at) VALUES (1, 'jwt', CURRENT_TIMESTAMP), (2, 'cleartext', CURRENT_TIMESTAMP), (3, 'ccache', NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillCredentialExpiryKnown(database); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var known []bool
	if err := database.Table("credential_entries").Order("id").Pluck("expiry_known", &known).Error; err != nil {
		t.Fatal(err)
	}
	if len(known) != 3 || !known[0] || known[1] || known[2] {
		t.Fatalf("expiry_known = %v, want [true false false]", known)
	}
}
