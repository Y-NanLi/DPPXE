//go:build windows && !servergui

package server

import "errors"

// RunNativeGUI 的占位实现（不带 servergui 标签的构建）。
//
// 控制端只有 PE 才需要原生界面，它依赖 lxn/walk（约 2.6MB）。
// 普通 Windows 构建不带 servergui，这两个依赖就不会被链接进来。
func RunNativeGUI(s *Server) error {
	return errors.New("此构建不含控制端原生界面（编译时需加 -tags servergui）")
}
