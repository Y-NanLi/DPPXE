package services

// FirewallRule 一条入站放行规则。
type FirewallRule struct {
	Name  string
	Proto string
	Ports string
}

// FirewallRules 需要放行的端口（与原软件一致）。
//
// 不放行的话，客户机能发来请求但收不到回包，表现就是
// "DHCP 没反应 / TFTP 超时 / 控制端连不上"。
var FirewallRules = []FirewallRule{
	{"DPPXE-DHCP", "UDP", "67,68"},
	{"DPPXE-TFTP", "UDP", "69"},
	{"DPPXE-HTTP", "TCP", "80"},
	{"DPPXE-FILESVC", "TCP", "6003"},
}
