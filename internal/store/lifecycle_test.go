package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCleanCloseLeavesSingleFile is the executable form of the single-file
// claim in ADR-0001: after a clean shutdown the data directory holds exactly
// one file.
func TestCleanCloseLeavesSingleFile(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "imchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, "a", "A", "hash", false); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "imchat.db" {
		t.Fatalf("clean shutdown should leave exactly one file, got %v", names)
	}
}
