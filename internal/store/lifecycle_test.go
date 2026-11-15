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
	// 整个测试就是一双眼睛：建库、写一条、正常关闭，然后数目录里有几个文件。
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "imchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	// 建库 → 迁移 → 写一个用户，然后正常关闭。
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, "a", "A", "hash", false); err != nil {
		t.Fatal(err)
	}
	// 关闭必须"干净"：WAL 被 checkpoint 掉，临时文件随之删除。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 只数文件名，不看内容：断言的就是「只有一个文件」这件事本身。
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "imchat.db" {
		t.Fatalf("clean shutdown should leave exactly one file, got %v", names)
	}
}
