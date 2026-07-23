package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// columnExists reports whether table has a column named col, via SQLite's
// table_info pragma.
func columnExists(t *testing.T, sqlDB *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := sqlDB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scanning pragma row: %v", err)
		}
		if name == col {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating pragma rows: %v", err)
	}
	return false
}

// TestMigration0041_NodeCPUCap_UpDown proves migration 0041 applies cleanly up
// AND down against a fresh DB: Up adds node_max_jobs.cpu_cap_percent, Down drops
// it, and a re-Up re-adds it (no residue that blocks re-migration).
func TestMigration0041_NodeCPUCap_UpDown(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("setting dialect: %v", err)
	}

	// Up to exactly 0041 (not goose.Up, which migrates to whatever the
	// latest migration happens to be — that made this test silently depend
	// on 0041 staying the newest file; it broke the moment 0042 landed,
	// since "Down one step" then rolled back 0042, not 0041). Pinning both
	// Up and Down to explicit versions makes this test about migration 0041
	// specifically, independent of how many migrations exist after it.
	if err := goose.UpTo(sqlDB, "migrations", 41); err != nil {
		t.Fatalf("goose UpTo 41: %v", err)
	}
	if !columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after Up, node_max_jobs.cpu_cap_percent should exist")
	}

	// Down to 0040: 0041 rolls back, dropping the column.
	if err := goose.DownTo(sqlDB, "migrations", 40); err != nil {
		t.Fatalf("goose DownTo 40: %v", err)
	}
	if columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after Down, node_max_jobs.cpu_cap_percent should be dropped")
	}

	// Up again: re-applies cleanly.
	if err := goose.UpTo(sqlDB, "migrations", 41); err != nil {
		t.Fatalf("goose UpTo 41 (re-apply): %v", err)
	}
	if !columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after re-Up, node_max_jobs.cpu_cap_percent should exist again")
	}
}
