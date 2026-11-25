package db

import (
	"strconv"
	"testing"

	"gorm.io/gorm"
)

const buildLogTenantMigrationID = "2026-11-25-build-logs-tenant-id"

func runBuildLogTenantMigration(t *testing.T, database *gorm.DB) {
	t.Helper()
	if err := findMigration(t, buildLogTenantMigrationID).Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func buildLogTenantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database := openTestDB(t)
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// Migration helpers use unqualified table names. One connection keeps the
	// temporary legacy tables visible to every query in this test.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	stmts := []string{
		`CREATE TEMP TABLE listeners (id INTEGER PRIMARY KEY, tenant_id INTEGER)`,
		`CREATE TEMP TABLE build_logs (id INTEGER PRIMARY KEY, listener_id INTEGER, created_at TIMESTAMP)`,
		`INSERT INTO listeners (id, tenant_id) VALUES (10, 7), (11, 9), (12, 0), (13, NULL)`,
		`INSERT INTO build_logs (id, listener_id) VALUES
			(1, 10), (2, 11), (3, 12), (4, 13), (5, 0), (6, NULL), (7, 999)`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}
	return database
}

func TestBuildLogTenantBackfillFromListenerMigration(t *testing.T) {
	database := buildLogTenantTestDB(t)
	runBuildLogTenantMigration(t, database)

	want := map[uint]uint{1: 7, 2: 9, 3: 0, 4: 0, 5: 0, 6: 0, 7: 0}
	for id, tenantID := range want {
		if got := tenantOf(t, database, "build_logs", strconv.Itoa(int(id))); got != tenantID {
			t.Fatalf("build log %d tenant = %d, want %d", id, got, tenantID)
		}
	}
	if !database.Migrator().HasIndex("build_logs", "idx_build_logs_tenant_created") {
		t.Fatal("idx_build_logs_tenant_created missing after migration")
	}

	// Re-running heals a NULL and does not overwrite an already-attributed row.
	if err := database.Exec("UPDATE build_logs SET tenant_id = NULL WHERE id = 2").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("UPDATE build_logs SET tenant_id = 42 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	runBuildLogTenantMigration(t, database)
	if got := tenantOf(t, database, "build_logs", "2"); got != 9 {
		t.Fatalf("second migrate left build log 2 at tenant %d, want 9", got)
	}
	if got := tenantOf(t, database, "build_logs", "1"); got != 42 {
		t.Fatalf("second migrate overwrote build log 1 tenant with %d, want 42", got)
	}
}

func TestBuildLogTenantMigrationRollback(t *testing.T) {
	database := buildLogTenantTestDB(t)
	mig := findMigration(t, buildLogTenantMigrationID)
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := mig.Rollback(database); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if database.Migrator().HasColumn("build_logs", "tenant_id") {
		t.Fatal("rollback kept build_logs.tenant_id")
	}
	if database.Migrator().HasIndex("build_logs", "idx_build_logs_tenant_created") {
		t.Fatal("rollback kept idx_build_logs_tenant_created")
	}
}

func TestBuildLogTenantMigrationSkipsMissingTables(t *testing.T) {
	database := openTestDB(t)
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE SCHEMA build_log_missing_table_test").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO build_log_missing_table_test").Error; err != nil {
			return err
		}
		if err := findMigration(t, buildLogTenantMigrationID).Migrate(tx); err != nil {
			return err
		}
		if tx.Migrator().HasTable("build_logs") {
			t.Fatal("migration created build_logs on a database without the table")
		}
		return tx.Exec("DROP SCHEMA build_log_missing_table_test CASCADE").Error
	}); err != nil {
		t.Fatalf("missing-table migration: %v", err)
	}
}
