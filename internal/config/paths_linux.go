//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

// ConfigDir 配置目录：$XDG_CONFIG_HOME/DPPXEGo，未设置时用 ~/.config/DPPXEGo。
//
// 不放在程序目录旁边：Linux 上程序通常在 /usr/local/bin，
// 普通用户没有写权限，配置会静默丢失。
func ConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			base = "."
		} else {
			base = filepath.Join(home, ".config")
		}
	}
	d := filepath.Join(base, "DPPXEGo")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// legacyPath Python 原版只在 Windows 上跑过，Linux 侧没有需要迁移的旧配置。
func legacyPath() string { return "" }
