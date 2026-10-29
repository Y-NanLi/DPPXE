package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// TaskName 计划任务名。
const TaskName = "DP-PXEClient"

func installDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	d := filepath.Join(base, "DP-PXEClient")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// Install 装成开机自启（SYSTEM 权限）。
//
// 用「计划任务(开机触发, SYSTEM) + Run 键」双保险。
func Install(server string, port int) int {
	if server == "" {
		fmt.Println("安装需要 --server 指定服务端 IP")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("找不到程序自身路径")
		return 1
	}
	dir := installDir()
	runner := filepath.Join(dir, "run.cmd")
	content := "@echo off\r\n" +
		fmt.Sprintf(`"%s" --client --server %s --port %d --quiet`+"\r\n", exe, server, port)
	if err := os.WriteFile(runner, []byte(content), 0o644); err != nil {
		fmt.Println("写启动脚本失败:", err)
		return 1
	}

	// 1) 计划任务：开机以 SYSTEM 运行
	cmd := exec.Command("schtasks", "/create", "/tn", TaskName, "/tr", runner,
		"/sc", "onstart", "/ru", "SYSTEM", "/rl", "HIGHEST", "/f")
	hideWindow(cmd)
	out, errTask := cmd.CombinedOutput()

	// 2) Run 键兜底（用户登录时也会拉起）
	okRun := false
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.WRITE); err == nil {
		if err := k.SetStringValue(TaskName, `"`+runner+`"`); err == nil {
			okRun = true
		}
		_ = k.Close()
	}

	fmt.Println("启动脚本：", runner)
	if errTask == nil {
		fmt.Println("计划任务：成功")
	} else {
		fmt.Println("计划任务：失败", string(out))
	}
	fmt.Println("Run 键  ：", map[bool]string{true: "成功", false: "失败"}[okRun])
	if errTask == nil || okRun {
		return 0
	}
	return 1
}

// Uninstall 卸载开机自启并清理安装目录。
func Uninstall() int {
	cmd := exec.Command("schtasks", "/delete", "/tn", TaskName, "/f")
	hideWindow(cmd)
	_ = cmd.Run()

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(TaskName)
		_ = k.Close()
	}
	_ = os.RemoveAll(installDir())
	fmt.Println("已卸载")
	return 0
}
