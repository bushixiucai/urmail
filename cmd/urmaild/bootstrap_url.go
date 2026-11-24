package main

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// logAdminBootstrap emits the one-time administrator bootstrap URL created by
// the container entrypoint (ARCH-PLAN §1.3) and then removes the on-disk copy,
// so the bearer token appears in exactly one startup log line. Subsequent boots
// find no file and print nothing; the token hash remains in the SQLite
// firstboot_state table for audit.
func logAdminBootstrap(logger *zap.Logger) {
	dir := os.Getenv("URMAIL_CONFIG_DIR")
	if dir == "" {
		dir = "/srv/urmail/config"
	}
	path := os.Getenv("URMAIL_ADMIN_BOOTSTRAP_FILE")
	if path == "" {
		path = filepath.Join(dir, "admin-bootstrap.url")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("admin bootstrap URL unreadable", zap.Error(err))
		}
		return
	}
	url := strings.TrimSpace(string(raw))
	if url == "" {
		return
	}
	logger.Info("admin bootstrap URL (one-time)", zap.String("url", url))
	if err := os.Remove(path); err != nil {
		logger.Warn("remove one-time admin bootstrap URL failed", zap.Error(err))
	}
}
