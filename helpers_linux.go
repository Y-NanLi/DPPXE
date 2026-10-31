//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// ensureConsole Linux 上进程本来就带标准输入输出，不需要补控制台。
func ensureConsole() {}

// fixConsoleCodepage Linux 终端默认就是 UTF-8，不需要切代码页。
func fixConsoleCodepage() {}

// pauseIfConsole 出错时停一下，避免从终端启动时窗口一闪而过。
func pauseIfConsole() {
	if os.Stdin == nil {
		return
	}
	fmt.Print("\n按回车键退出…")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// openBrowser 用 xdg-open 打开默认浏览器。
func openBrowser(url string) {
	cmd := exec.Command("xdg-open", url)
	_ = cmd.Start()
}
