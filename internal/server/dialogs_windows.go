package server

import (
	"os/exec"
	"strings"
	"syscall"
)

// hideWindow 启动子进程时不弹黑窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// runPowerShell 执行一段 PowerShell（STA，用于弹系统对话框）。
func runPowerShell(script string) (string, error) {
	full := "[Console]::OutputEncoding=[Text.Encoding]::UTF8\n" +
		"Add-Type -AssemblyName System.Windows.Forms\n" + script
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", full)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), nil
}

// PickFolder 弹出"选择文件夹"对话框，取消返回空串。
func PickFolder(title, initial string) (string, error) {
	script := `
$f = New-Object System.Windows.Forms.FolderBrowserDialog
$f.Description = ` + psQuote(title) + `
$f.ShowNewFolderButton = $true
`
	if initial != "" {
		script += `$f.SelectedPath = ` + psQuote(initial) + "\n"
	}
	script += `if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($f.SelectedPath) }`
	return runPowerShell(script)
}

// PickFiles 弹出"选择文件"对话框（可多选）。
func PickFiles(title string) ([]string, error) {
	out, err := runPowerShell(`
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = ` + psQuote(title) + `
$d.Multiselect = $true
$d.Filter = '所有文件 (*.*)|*.*'
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write(($d.FileNames -join [char]10)) }
`)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// PickFile 弹出"选择单个文件"对话框。
func PickFile(title, initial, filter string) (string, error) {
	if filter == "" {
		filter = "所有文件 (*.*)|*.*"
	}
	return runPowerShell(`
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = ` + psQuote(title) + `
$d.Filter = ` + psQuote(filter) + `
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.FileName) }
`)
}

// PickSave 弹出"另存为"对话框。
func PickSave(title, defaultName, filter string) (string, error) {
	if filter == "" {
		filter = "所有文件 (*.*)|*.*"
	}
	return runPowerShell(`
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.Title = ` + psQuote(title) + `
$d.FileName = ` + psQuote(defaultName) + `
$d.Filter = ` + psQuote(filter) + `
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.FileName) }
`)
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
