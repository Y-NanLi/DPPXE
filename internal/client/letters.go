package client

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// 盘符冲突处理。
//
// 现象（真实踩到过）：分区脚本里 `assign letter=D` 报
//   "虚拟磁盘服务错误: 指定的驱动器号对于分配不可用"
// diskpart 遇到这个错误会**中止整个脚本**，于是后面的分区/格式化一个都不执行，
// 表现就是"选了 3 个分区，结果只出来 1 个"。
//
// 原因：PE 里 C/D/E 常被光驱、U 盘或旧分区占着。所以执行整盘分区前要做三件事：
//  1. 先把 C..Z 上能摘掉的盘符摘掉（跳过 A/B、PE 自己的系统盘、程序所在盘）；
//  2. 摘不掉的（多半是光驱）试着把它挪到靠后的字母去；
//  3. 如果脚本要的盘符还是被占，就把脚本里的 assign letter 按顺序换成真正可用的盘符。

const assignableLetters = "CDEFGHIJKLMNOPQRSTUVWXYZ"

var (
	reAssignLetter = regexp.MustCompile(`(?i)^(\s*assign\s+letter\s*=\s*)([A-Za-z])\s*$`)
	reSelectVolume = regexp.MustCompile(`(?i)^(\s*select\s+volume\s+)([A-Za-z])\s*$`)
)

// driveLettersToFree 从盘符位图里挑出需要腾出来的盘符（跳过 A/B、系统盘、程序所在盘）。
func driveLettersToFree(mask uint32, sysDrive, selfDrive string) []string {
	sysDrive = strings.ToUpper(strings.TrimSpace(sysDrive))
	selfDrive = strings.ToUpper(strings.TrimSpace(selfDrive))
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		L := string(rune('A' + i))
		if L == "A" || L == "B" || L == sysDrive || L == selfDrive {
			continue
		}
		out = append(out, L)
	}
	return out
}

// freeLetterList 返回当前空闲、可用于数据分区的盘符（C 起）。
func freeLetterList(mask uint32, sysDrive, selfDrive string) []string {
	sysDrive = strings.ToUpper(strings.TrimSpace(sysDrive))
	selfDrive = strings.ToUpper(strings.TrimSpace(selfDrive))
	var out []string
	for _, ch := range assignableLetters {
		L := string(ch)
		idx := int(ch - 'A')
		if L == sysDrive || L == selfDrive {
			continue // 系统盘 / 程序所在盘不能拿去分区
		}
		if mask&(1<<uint(idx)) != 0 {
			continue // 已被占用
		}
		out = append(out, L)
	}
	return out
}

// rewriteAssignLetters 把脚本里的 assign letter 按出现顺序换成 picked，
// 并同步后面的 `select volume <旧盘符>`（卷标那一段是按盘符找卷的）。
func rewriteAssignLetters(script string, picked []string) (string, map[string]string) {
	lines := strings.Split(script, "\n")
	mapping := map[string]string{}
	idx := 0
	for i, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r" // 保留 CRLF，避免"没改动也变样"
		}
		raw := strings.TrimRight(line, "\r")
		if m := reAssignLetter.FindStringSubmatch(raw); m != nil {
			old := strings.ToUpper(m[2])
			if idx < len(picked) {
				newL := picked[idx]
				if _, done := mapping[old]; !done {
					mapping[old] = newL
				}
				lines[i] = m[1] + newL + cr
				idx++
			}
			continue
		}
	}
	if len(mapping) == 0 {
		return script, mapping
	}
	for i, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		raw := strings.TrimRight(line, "\r")
		if m := reSelectVolume.FindStringSubmatch(raw); m != nil {
			old := strings.ToUpper(m[2])
			if newL, ok := mapping[old]; ok && newL != old {
				lines[i] = m[1] + newL + cr
			}
		}
	}
	return strings.Join(lines, "\n"), mapping
}

// exeDriveLetter 程序自身所在盘（腾盘符时要跳过它）。
func exeDriveLetter() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	v := filepath.VolumeName(exe) // "C:"
	if len(v) < 2 {
		return ""
	}
	return strings.ToUpper(v[:1])
}

// writeDiskpartScript 把脚本写到"确实可写"的目录，返回文件路径。
//
// PE 里客户端是开机时由 PECMD 以 SYSTEM 拉起的，此时 %TEMP% 可能指向一个
// 还不存在的用户目录（X:\Users\Default\AppData\Local\Temp），写文件会静默失败，
// diskpart 随后就报"无法打开或读取脚本文件"。所以这里按优先级逐个试，
// 并回读确认文件真的写进去了。
func (e *Engine) writeDiskpartScript(script string) (string, error) {
	if strings.TrimSpace(script) == "" {
		return "", fmt.Errorf("脚本内容为空")
	}
	var dirs []string
	if t := os.Getenv("TEMP"); t != "" {
		dirs = append(dirs, t)
	}
	if w := os.Getenv("SystemRoot"); w != "" {
		dirs = append(dirs, filepath.Join(w, "Temp")) // PE 里一定有 X:\Windows\Temp
	}
	dirs = append(dirs, e.client.ExeDir(), os.TempDir(), ".")

	name := fmt.Sprintf("DPPXE_DP_%d.txt", time.Now().UnixNano()%1000000000)
	data := encodeGBK(script)
	var lastErr error
	for _, d := range dirs {
		if d == "" {
			continue
		}
		_ = os.MkdirAll(d, 0o755)
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			lastErr = err
			continue
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			lastErr = fmt.Errorf("写入后文件为空: %s", p)
			continue
		}
		if d != os.Getenv("TEMP") {
			e.log("TEMP 目录不可写，diskpart 脚本改写到 " + d)
		}
		return p, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可写目录")
	}
	return "", fmt.Errorf("%v（TEMP=%q）", lastErr, os.Getenv("TEMP"))
}

// runDiskpartText 跑一段 diskpart 脚本，返回 (返回码, 输出)。
func (e *Engine) runDiskpartText(script string, timeout int) (int, string) {
	p, err := e.writeDiskpartScript(script)
	if err != nil {
		e.log("写 diskpart 脚本失败：" + err.Error())
		return -1, "无法写入脚本文件：" + err.Error()
	}
	e.lastScriptPath = p
	return e.Run(fmt.Sprintf(`diskpart /s "%s"`, p), true, timeout)
}

// wantedLetters 脚本里 assign letter 依次要用的盘符。
func wantedLetters(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		if m := reAssignLetter.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			out = append(out, strings.ToUpper(m[2]))
		}
	}
	return out
}

// occupied 位图里该盘符是否已被占用。
func occupied(mask uint32, L string) bool {
	if len(L) != 1 {
		return false
	}
	return mask&(1<<uint(L[0]-'A')) != 0
}

// prepareDriveLetters 腾盘符 + 按需改写脚本里的 assign letter。
//
// 只处理脚本真正要用的那几个盘符（一般就是 C/D/E），不动其它磁盘的盘符。
// 返回改写后的脚本；日志里说明每一步做了什么。
func (e *Engine) prepareDriveLetters(script string) string {
	if e.dryRun {
		return script
	}
	wanted := wantedLetters(script)
	if len(wanted) == 0 {
		return script
	}
	sys := strings.ToUpper(strings.TrimSuffix(os.Getenv("SystemDrive"), ":"))
	self := exeDriveLetter()

	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return script
	}

	// 1) 先把脚本要用的盘符腾出来（被占的才处理）
	var needFree []string
	for _, L := range wanted {
		if L == sys || L == self || !occupied(mask, L) {
			continue
		}
		if !containsStr(needFree, L) {
			needFree = append(needFree, L)
		}
	}
	if len(needFree) > 0 {
		e.log("分区前先腾出盘符：" + strings.Join(needFree, " "))
	}
	for _, L := range needFree {
		rc, out := e.runDiskpartText("select volume "+L+"\r\nremove letter="+L+"\r\nexit\r\n", 120)
		if rc == 0 {
			continue
		}
		// 2) 摘不掉（多半是光驱）：把它挪到靠后的字母去
		if moved := e.moveLetterAway(mask, L); moved != "" {
			e.log(fmt.Sprintf("  盘符 %s: 无法移除，已改挂到 %s", L, moved))
			mask, _ = windows.GetLogicalDrives()
		} else {
			reason := firstLine(out)
			if reason == "" {
				reason = "diskpart 无输出"
			}
			e.log(fmt.Sprintf("  盘符 %s: 无法移除（%s），将改用其它空闲盘符", L, reason))
		}
	}
	mask, _ = windows.GetLogicalDrives()

	// 3) 按"当前真正可用的盘符"重新分配：脚本要几个就给几个
	free := freeLetterList(mask, sys, self)
	if len(free) < len(wanted) {
		e.log("可用盘符不足，保持脚本原样（请手动检查盘符占用）")
		return script
	}
	picked := free[:len(wanted)]
	newScript, mapping := rewriteAssignLetters(script, picked)

	changed := false
	for old, newL := range mapping {
		if old != newL {
			changed = true
			break
		}
	}
	if changed {
		e.log(fmt.Sprintf("盘符冲突：数据分区改用 %s（原计划 %s）",
			strings.Join(picked, "/"), strings.Join(wanted, "/")))
	} else {
		e.log("数据分区盘符：" + strings.Join(picked, "/"))
	}
	return newScript
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// moveLetterAway 把某个被占用的盘符挪到 Z 往前的空闲字母上，成功返回新盘符。
func (e *Engine) moveLetterAway(mask uint32, from string) string {
	for i := 25; i >= 0; i-- {
		L := string(rune('A' + i))
		if L == from || L == "A" || L == "B" {
			continue
		}
		if mask&(1<<uint(i)) != 0 {
			continue
		}
		rc, _ := e.runDiskpartText("select volume "+from+"\r\nassign letter="+L+"\r\nexit\r\n", 120)
		if rc == 0 {
			return L
		}
		return ""
	}
	return ""
}

// firstLine 取输出里前两行非空内容（diskpart 的报错常是"标题 + 原因"两行）。
func firstLine(s string) string {
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			parts = append(parts, t)
			if len(parts) == 2 {
				break
			}
		}
	}
	out := strings.Join(parts, " ")
	if len([]rune(out)) > 60 {
		return string([]rune(out)[:60])
	}
	return out
}
