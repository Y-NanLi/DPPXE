//go:build windows && !clientgui

package client

import "errors"

// RunNativeGUI 的占位实现（不带 clientgui 标签的构建）。
//
// 只有**被控端**需要 PE 里的原生界面，它依赖 lxn/walk + lxn/win（约 2MB）。
// 控制端 `DP-PXE.exe` 用不到，所以编译控制端时不加 `clientgui` 标签，
// 这两个依赖就不会被链接进来。
func RunNativeGUI(c *Client) error {
	return errors.New("此构建不含 PE 原生界面（编译时需加 -tags clientgui）")
}
