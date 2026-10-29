// Package services 网络服务集合：DHCP / TFTP / HTTP / 控制通道 / 服务发现 / WOL。
package services

// LogFunc 统一日志回调（kind: info / ok / warn / err）。
type LogFunc func(msg, kind string)

func (f LogFunc) log(msg string)           { f(msg, "info") }
func (f LogFunc) logKind(msg, kind string) { f(msg, kind) }
