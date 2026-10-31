//go:build !windows

package services

import (
	"fmt"
	"os/exec"
	"strings"
)

// AddFirewallRules Linux 侧的端口放行。
//
// Linux 没有 Windows 那种"一个 API 加规则"的入口，只能按发行版各走各的：
//   - firewalld（RHEL / CentOS / Fedora / openEuler）→ firewall-cmd
//   - ufw（Ubuntu / Debian）→ ufw
//   - 两者都没有 → 不猜、不硬改，只把端口列出来让用户自己配
//
// 三种情况都需要 root 权限；普通用户跑会失败，这是预期行为。
// 返回 (成功条数, 汇总信息)，与 Windows 版保持同样的签名。
func AddFirewallRules() (int, string) {
	if _, err := exec.LookPath("firewall-cmd"); err == nil {
		return addFirewalld()
	}
	if _, err := exec.LookPath("ufw"); err == nil {
		return addUfw()
	}
	return 0, "未检测到 firewalld/ufw，请手动放行：" + portHint()
}

// addFirewalld 用 firewalld 永久规则 + reload。
func addFirewalld() (int, string) {
	ok := 0
	var msgs []string
	for _, r := range FirewallRules {
		for _, p := range strings.Split(r.Ports, ",") {
			spec := fmt.Sprintf("%s/%s", strings.TrimSpace(p), strings.ToLower(r.Proto))
			cmd := exec.Command("firewall-cmd", "--permanent", "--add-port="+spec)
			hideWindow(cmd)
			if out, err := cmd.CombinedOutput(); err == nil {
				ok++
			} else {
				msgs = append(msgs, fmt.Sprintf("%s: %s", spec, clip(string(out))))
			}
		}
	}
	// 永久规则必须 reload 才生效，否则防火墙里查得到、实际不放行。
	cmd := exec.Command("firewall-cmd", "--reload")
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		msgs = append(msgs, "reload 失败: "+clip(string(out)))
	}
	if len(msgs) == 0 {
		return ok, "全部成功（firewalld）"
	}
	return ok, strings.Join(msgs, "; ")
}

// addUfw 用 ufw 加规则。
func addUfw() (int, string) {
	ok := 0
	var msgs []string
	for _, r := range FirewallRules {
		for _, p := range strings.Split(r.Ports, ",") {
			spec := fmt.Sprintf("%s/%s", strings.TrimSpace(p), strings.ToLower(r.Proto))
			cmd := exec.Command("ufw", "allow", spec)
			hideWindow(cmd)
			if out, err := cmd.CombinedOutput(); err == nil {
				ok++
			} else {
				msgs = append(msgs, fmt.Sprintf("%s: %s", spec, clip(string(out))))
			}
		}
	}
	if len(msgs) == 0 {
		return ok, "全部成功（ufw）"
	}
	return ok, strings.Join(msgs, "; ")
}

// portHint 把规则列表拼成一句人话。
func portHint() string {
	var parts []string
	for _, r := range FirewallRules {
		parts = append(parts, r.Proto+" "+r.Ports)
	}
	return strings.Join(parts, " / ")
}

// clip 截断子进程输出，避免日志被一大段帮助文本淹没。
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
