package main

import (
	"os"
	"os/signal"
)

// signalNotify 订阅 Ctrl+C。
func signalNotify(ch chan os.Signal) {
	signal.Notify(ch, os.Interrupt)
}
