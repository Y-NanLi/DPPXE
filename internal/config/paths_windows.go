//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// ConfigDir 配置目录：%APPDATA%\DPPXEGo。
func ConfigDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			base = "."
		}
	}
	d := filepath.Join(base, "DPPXEGo")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// legacyPath Python 版的配置位置，用于一次性迁移。
func legacyPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			return ""
		}
	}
	return filepath.Join(base, "HHXSPxeRebuild", "config.json")
}
