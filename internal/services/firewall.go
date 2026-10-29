package services

import (
	"fmt"
	"os/exec"
	"strings"
)

// FirewallRule 一条入站放行规则。
type FirewallRule struct {
	Name  string
	Proto string
	Ports string
}

// FirewallRules 需要放行的端口（与原软件一致）。
var FirewallRules = []FirewallRule{
	{"DPPXE-DHCP", "UDP", "67,68"},
	{"DPPXE-TFTP", "UDP", "69"},
	{"DPPXE-HTTP", "TCP", "80"},
	{"DPPXE-FILESVC", "TCP", "6003"},
}

// AddFirewallRules Windows 防火墙放行（等价于原软件用 INetFwPolicy2 加规则）。
//
// 服务端如果不放行 67/69/80，虚拟机能发来请求但收不到回包，
// 表现就是"DHCP 没反应 / TFTP 超时"。返回 (成功条数, 汇总信息)。
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
