//go:build !windows

package server

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// hideWindow 非 Windows 平台没有"黑窗口"概念，留空即可。
func hideWindow(cmd *exec.Cmd) {}

// pickerCmd 返回一个可用的图形选择器命令，都没有则返回空串。
//
// Linux 上没有统一的系统对话框 API，只能借 zenity / kdialog。
// 两者通常不会同时存在（GTK 桌面装 zenity、KDE 桌面装 kdialog）。
func pickerCmd() string {
	for _, n := range []string{"zenity", "kdialog"} {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	return ""
}

// errNoPicker 没有图形选择器时的提示。
var errNoPicker = errors.New("系统没有 zenity/kdialog，请直接手动输入路径")

// run 执行选择器并返回标准输出。
func runPicker(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		// 用户点"取消"时 zenity 返回退出码 1，这里按"取消"处理，不当作错误刷屏。
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PickFolder 弹出"选择文件夹"对话框，取消返回空串。
func PickFolder(title, initial string) (string, error) {
	p := pickerCmd()
	if p == "" {
		return "", errNoPicker
	}
	if p == "kdialog" {
		args := []string{"--getexistingdirectory"}
		if initial != "" {
			args = append(args, initial)
		}
		return runPicker(p, args...)
	}
	args := []string{"--file-selection", "--directory", "--title=" + title}
	if initial != "" {
		args = append(args, "--filename="+strings.TrimSuffix(initial, "/")+"/")
	}
	return runPicker(p, args...)
}

// PickFiles 弹出"选择文件"对话框（可多选）。
func PickFiles(title string) ([]string, error) {
	p := pickerCmd()
	if p == "" {
		return nil, errNoPicker
	}
	var out string
	var err error
	if p == "kdialog" {
		out, err = runPicker(p, "--getopenfilename", "--multiple", "--separate-output")
	} else {
		out, err = runPicker(p, "--file-selection", "--multiple", "--separator=\n", "--title="+title)
	}
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// PickFile 弹出"选择单个文件"对话框。
func PickFile(title, initial, filter string) (string, error) {
	p := pickerCmd()
	if p == "" {
		return "", errNoPicker
	}
	if p == "kdialog" {
		return runPicker(p, "--getopenfilename", initial, filter)
	}
	args := []string{"--file-selection", "--title=" + title}
	if initial != "" {
		args = append(args, "--filename="+initial)
	}
	return runPicker(p, args...)
}

// PickSave 弹出"另存为"对话框。
func PickSave(title, defaultName, filter string) (string, error) {
	p := pickerCmd()
	if p == "" {
		return "", errNoPicker
	}
	if p == "kdialog" {
		return runPicker(p, "--getsavefilename", defaultName, filter)
	}
	args := []string{"--file-selection", "--save", "--confirm-overwrite",
		"--title=" + title, "--filename=" + defaultName}
	return runPicker(p, args...)
}

// openPath 用系统文件管理器打开一个目录。
func openPath(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	cmd := exec.Command("xdg-open", dir)
	return cmd.Start()
}
