//go:build windows

package services

import (
	"fmt"
	"os/exec"
	"strings"
)

// AddFirewallRules Windows 防火墙放行（等价于原软件用 INetFwPolicy2 加规则）。
//
// 返回 (成功条数, 汇总信息)。
func AddFirewallRules() (int, string) {
	ok := 0
	var msgs []string
	for _, r := range FirewallRules {
		cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
			"name="+r.Name, "dir=in", "action=allow",
			"protocol="+r.Proto, "localport="+r.Ports, "profile=any")
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err == nil {
			ok++
		} else {
			text := strings.TrimSpace(string(out))
			if len(text) > 80 {
				text = text[:80]
			}
			msgs = append(msgs, fmt.Sprintf("%s: %s", r.Name, text))
		}
	}
	if len(msgs) == 0 {
		return ok, "全部成功"
	}
	return ok, strings.Join(msgs, "; ")
}
