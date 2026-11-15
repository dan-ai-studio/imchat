package main

// 定时备份：每天把 SQLite 复制一份到 backups/，保留最近七份。
// 备份是「能恢复」的前提，不是「有文件」——恢复演练记录在 docs/DEPLOY.md。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"imchat/internal/store"
)

// dailyBackup 每隔 24 小时做一次备份，启动时先做一次。
// 备份失败只记日志：备份不该把主进程拖垮。
func dailyBackup(ctx context.Context, db *store.DB, dir string) {
	if dir == "" {
		return
	}
	run := func() {
		// 文件名带日期：同一天跑两次会覆盖，不会堆积。
		name := filepath.Join(dir, "imchat-"+time.Now().Format("2006-01-02")+".db")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logf("backup: mkdir %s: %v", dir, err)
			return
		}
		// 备份失败只记日志：备份不该把主进程拖垮。
		if err := db.Backup(ctx, name); err != nil {
			logf("backup: %v", err)
			return
		}
		pruneBackups(dir, 7)
		logf("backup: wrote %s", name)
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	// 每 24 小时一次；ctx 取消就停，不留后台 goroutine。
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// pruneBackups keeps the newest keep files and deletes the rest.
func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// 只收普通文件：目录与临时文件不参与保留策略。
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	// 名字带日期，字典序就是时间序：删掉前面多出来的。
	for _, name := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// logf is the backup path's only logger: it must never panic.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
