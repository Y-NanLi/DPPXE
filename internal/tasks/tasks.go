// Package tasks 指令构造：把 UI 参数翻译成被控端真正要执行的命令 / 脚本。
//
// 与 Python 版 app/tasks.py 一一对应，输出文本逐字符一致：
//   - 分区      -> DiskPart 脚本（diskpart /S）
//   - 引导修复  -> format + bcdboot
//   - 还原/备份 -> wimlib apply / capture（脚本里的 UNCOMPRESS / COMPRESS）
//   - 流式安装  -> STREAMINSTALL 参数串
//   - 自动化    -> 脚本引擎文本（ALL/PE/SYS 前缀）
package tasks

import (
	"fmt"
	"strconv"
	"strings"
)

// MB 1GB = 1024MB（与原软件一致）。
const MB = 1024

// PartitionSpec 对齐 DiskGenius「快速分区」的参数模型。
type PartitionSpec struct {
	Disk         int      `json:"disk"`
	Table        string   `json:"table"`         // gpt / mbr
	TotalGB      int      `json:"total_gb"`      // 估算总容量，只用于算默认大小
	Count        int      `json:"count"`         // 数据分区个数
	PrimaryCount int      `json:"primary_count"` // MBR 主分区数（1-4），其余为逻辑分区（Primaries 为空时的回退）
	Primaries    []bool   `json:"primaries"`     // 每个分区是否主分区（MBR 用；必须前缀）
	SizesGB      []int    `json:"sizes_gb"`
	Labels       []string `json:"labels"`
	Filesystems  []string `json:"filesystems"`
	RebuildMBR   bool     `json:"rebuild_mbr"`
	KeepESP      bool     `json:"keep_esp"`
	CreateESP    bool     `json:"create_esp"`
	CreateMSR    bool     `json:"create_msr"`
	EspMB        int      `json:"esp_mb"`
	MsrMB        int      `json:"msr_mb"`
	Align        int      `json:"align"` // 对齐扇区数，0 = 不对齐
}

// DefaultLabels 默认卷标。
var DefaultLabels = []string{"系统", "软件", "文档", "数据", "备份", "其他"}

// NewSpec 返回一个默认的分区参数（GPT / 3 分区 / 60GB）。
func NewSpec() *PartitionSpec {
	sp := &PartitionSpec{
		Disk: 0, Table: "gpt", TotalGB: 60, Count: 3,
		CreateESP: true, CreateMSR: true,
		EspMB: 300, MsrMB: 16, Align: 2048,
		Labels:      append([]string{}, DefaultLabels[:3]...),
		Filesystems: []string{"NTFS", "NTFS", "NTFS"},
	}
	sp.SizesGB = DefaultSizes(60, 3, "gpt", 300, 16, true, true)
	return sp
}

// DefaultSizes 按总容量均分（最后一个吃掉零头），单位 GB。
func DefaultSizes(totalGB, count int, table string, espMB, msrMB int, createESP, createMSR bool) []int {
	if count < 1 {
		count = 1
	}
	if count > 6 {
		count = 6
	}
	usable := totalGB * MB
	if usable < 4096 {
		usable = 4096
	}
	if table == "gpt" {
		if createESP {
			usable -= espMB
		}
		if createMSR {
			usable -= msrMB
		}
	}
	usable -= 32 // 预留一点，避免边界失败
	perGB := usable / count / MB
	if perGB < 1 {
		perGB = 1
	}
	sizes := make([]int, 0, count)
	for i := 0; i < count-1; i++ {
		sizes = append(sizes, perGB)
	}
	last := usable/MB - perGB*(count-1)
	if last < 1 {
		last = 1
	}
	return append(sizes, last)
}

func resolveTable(sp *PartitionSpec) string {
	if sp.Table == "mbr" {
		return "mbr"
	}
	return "gpt"
}

func at(ss []string, i int) string {
	if i >= 0 && i < len(ss) {
		return ss[i]
	}
	return ""
}

func atInt(ss []int, i int) int {
	if i >= 0 && i < len(ss) {
		return ss[i]
	}
	return 0
}

// BuildDiskpart 生成 DiskPart 脚本（对齐 DiskGenius 快速分区的行为）。
func BuildDiskpart(sp *PartitionSpec) string {
	table := resolveTable(sp)
	count := sp.Count
	if count < 1 {
		count = 1
	}
	if count > 6 {
		count = 6
	}
	const letters = "CDEFGHIJKLMNOPQRSTUVWXYZ"
	align := ""
	if sp.Align > 0 {
		align = fmt.Sprintf(" align=%d", sp.Align)
	}

	lines := []string{
		fmt.Sprintf("rem DP-PXE partition script  disk=%d table=%s count=%d align=%d",
			sp.Disk, table, count, sp.Align),
		fmt.Sprintf("Select disk %d", sp.Disk),
	}
	if sp.KeepESP {
		lines = append(lines, "rem 保留现有 ESP：跳过 Clean，只在未分配空间上建分区")
	} else {
		lines = append(lines, "Clean")
	}
	lines = append(lines, "convert "+table)

	createESP := sp.CreateESP
	createMSR := sp.CreateMSR
	espMB := sp.EspMB
	if espMB == 0 {
		espMB = 300
	}
	msrMB := sp.MsrMB
	if msrMB == 0 {
		msrMB = 16
	}

	if table == "gpt" && !sp.KeepESP {
		if createESP {
			// 不给 ESP 分配盘符：PE 里盘符容易被占，且 bcdboot 会自动找 ESP
			lines = append(lines,
				fmt.Sprintf("create partition efi size=%d", espMB),
				`format quick fs=Fat32 label="ESP"`)
		}
		if createMSR {
			lines = append(lines, fmt.Sprintf("create partition msr size=%d", msrMB))
		}
	}

	type labelPair struct{ letter, label string }
	var labels []labelPair
	partNo := 0 // 磁盘上的分区序号，用于显式 select
	if table == "gpt" && !sp.KeepESP {
		if createESP {
			partNo++
		}
		if createMSR {
			partNo++
		}
	}

	one := func(idx int, logical bool) []string {
		sizeGB := atInt(sp.SizesGB, idx)
		fs := at(sp.Filesystems, idx)
		if fs == "" {
			fs = "NTFS"
		}
		label := at(sp.Labels, idx)
		kind := "primary"
		if logical {
			kind = "logical"
		}
		last := idx == count-1
		cmd := "Create partition " + kind
		if !last && sizeGB > 0 {
			cmd += fmt.Sprintf(" size=%d%s", sizeGB*MB, align)
		} else if align != "" && !logical {
			cmd += align
		}
		out := []string{cmd}
		if table == "mbr" && idx == 0 && !logical {
			out = append(out, "active")
		}
		// 显式选中刚建的分区：create 之后选择集不一定是它（format 打到 MSR 上会报参数无效）
		partNo++
		out = append(out, fmt.Sprintf("select partition %d", partNo))
		letter := string(letters[min(idx, len(letters)-1)])
		out = append(out, "assign letter="+letter)
		out = append(out, "format quick fs="+fs)
		if label != "" {
			labels = append(labels, labelPair{letter, label})
		}
		return out
	}

	if table == "mbr" {
		primaries := resolveMBRPrimaries(sp, count)
		// 先建所有主分区（MBR 主分区必须排在扩展分区之前）
		for i := 0; i < count; i++ {
			if primaries[i] {
				lines = append(lines, one(i, false)...) // 第 1 个自动 active
			}
		}
		hasLogical := false
		for i := 0; i < count; i++ {
			if !primaries[i] {
				hasLogical = true
				break
			}
		}
		if hasLogical {
			lines = append(lines, "Create partition extended")
			partNo = 4 // MBR 逻辑分区从 5 开始编号
			for i := 0; i < count; i++ {
				if !primaries[i] {
					lines = append(lines, one(i, true)...) // 逻辑分区
				}
			}
		}
	} else {
		for i := 0; i < count; i++ {
			lines = append(lines, one(i, false)...)
		}
	}

	if len(labels) > 0 {
		lines = append(lines, "rem 卷标单独设置（放在最后，失败也不影响分区）")
		for _, lp := range labels {
			lines = append(lines, "select volume "+lp.letter)
			if strings.Contains(lp.label, " ") {
				lines = append(lines, fmt.Sprintf(`label "%s"`, lp.label))
			} else {
				lines = append(lines, "label "+lp.label)
			}
		}
	}

	if sp.RebuildMBR && table == "mbr" {
		lines = append(lines, "rem POST bootsect /nt60 SYS /mbr")
	}
	lines = append(lines, "exit")
	return strings.Join(lines, "\r\n")
}

// resolveMBRPrimaries 计算 MBR 每个分区是否主分区（必须前缀 true..false..）。
// 约束：全主分区时最多 4 个；有逻辑分区时必须留一个槽给扩展分区 → 最多 3 个。
func resolveMBRPrimaries(sp *PartitionSpec, count int) []bool {
	prim := make([]bool, count)
	if len(sp.Primaries) > 0 {
		copy(prim, sp.Primaries)
	} else {
		pc := sp.PrimaryCount
		if pc <= 0 {
			pc = 1
		}
		for i := 0; i < pc && i < count; i++ {
			prim[i] = true
		}
	}
	// 强制前缀：主分区必须是连续的
	pc := 0
	for pc < count && prim[pc] {
		pc++
	}
	maxPC := count
	if count > 4 {
		maxPC = 3 // 超过 4 个分区时必然有逻辑分区，需留槽给扩展
	}
	if pc > maxPC {
		pc = maxPC
	}
	out := make([]bool, count)
	for i := 0; i < pc; i++ {
		out[i] = true
	}
	return out
}

// BuildFixboot 引导修复：先格式化引导分区，再 bcdboot。
func BuildFixboot(winDir, bootPart, firmware string) []string {
	fw := "/f ALL"
	switch firmware {
	case "uefi":
		fw = "/f UEFI"
	case "legacy":
		fw = "/f BIOS"
	}
	return []string{
		fmt.Sprintf("Format %s /q /y", bootPart),
		fmt.Sprintf("bcdboot.exe %s /s %s %s /l zh-cn", winDir, bootPart, fw),
	}
}

// BuildRebuildMBR 重建主引导：只对 MBR 盘有效，GPT 盘会被跳过。
func BuildRebuildMBR(disk int) []string {
	return []string{
		fmt.Sprintf("rem 写入物理磁盘 %d 的 0 号扇区（MBR 引导代码 + 分区表校验）", disk),
		fmt.Sprintf("\\\\.\\PHYSICALDRIVE%d  ->  WriteFile(sector 0)", disk),
	}
}

// BuildApply 还原镜像。compact=true 即界面上的"压缩"（WIMBoot/Compact 部署）。
func BuildApply(image string, index int, target string, compact bool) string {
	flag := "FALSE"
	if compact {
		flag = "TRUE"
	}
	return fmt.Sprintf(`UNCOMPRESS "%s|%d" %s %s`, image, index, target, flag)
}

// BuildBackup 备份镜像。
func BuildBackup(source, image string, incremental bool) string {
	flag := "FALSE"
	if incremental {
		flag = "TRUE"
	}
	return fmt.Sprintf(`COMPRESS %s "%s" %s`, source, image, flag)
}

// BuildInjectDrv 注入驱动（等价 dism /Add-Driver /forceunsigned）。
func BuildInjectDrv(target, drvDir string) string {
	return fmt.Sprintf(`INJECTDRV %s "%s"`, target, drvDir)
}

// StreamOptions 流式安装的 8 个开关 + 硬盘号 / 分区。
type StreamOptions struct {
	URL       string
	PartIndex int
	AutoPart  bool
	Format    bool
	FixBoot   bool
	Unattend  bool
	PEDrv     bool
	Client    bool
	VNC       bool
	Reboot    bool
	Disk      int
	Compact   bool
}

// BuildStreamParams 生成 STREAMINSTALL 的 11 个参数位，顺序与真实软件一致。
//
//	URL  安装分区(1-24→C-Z)  智能分区  格式化  引导修复  注入应答
//	注入PE驱动  注入客户端  注入VNC  自动重启  硬盘号  [压缩]
func BuildStreamParams(o StreamOptions) string {
	bit := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	bits := []string{`"` + o.URL + `"`, strconv.Itoa(o.PartIndex)}
	for _, v := range []bool{o.AutoPart, o.Format, o.FixBoot, o.Unattend,
		o.PEDrv, o.Client, o.VNC, o.Reboot} {
		bits = append(bits, bit(v))
	}
	bits = append(bits, strconv.Itoa(o.Disk), bit(o.Compact))
	return strings.Join(bits, " ")
}

// PartIndexFromLetter "C:\" -> 3。
func PartIndexFromLetter(letter string) int {
	letter = strings.TrimRight(strings.TrimSpace(strings.ToUpper(letter)), ":\\/")
	if letter == "" {
		return 1
	}
	idx := int(letter[0]) - int('A') + 1
	if idx < 1 {
		return 1
	}
	if idx > 24 {
		return 24
	}
	return idx
}

// ScriptTemplate 自动化脚本模板（与 Python 版一致）。
const ScriptTemplate = `' DP-PXE 自动化脚本（前缀 ALL / PE / SYS 必填，单引号后为注释）
PE      Sleep              2000                          '等待2秒
PE      AutoPartition      0                             '自动给0号磁盘分区
PE      SETSAVEPATH        %MAXFree%:\Receive\           '保存到剩余空间最大的分区
PE      DOWNLOAD           http://%ServerIP%/Win10.ESD   '下载镜像
PE      uncompress         %SaveFilePath%Win10.ESD|1  C:\  False
PE      FixBoot            C:\            0    0        '修复引导
PE      injectClient       C:\                           '注入客户端
PE      injectAuto         C:\                           '注入自动应答
PE      injectVNC          C:\            TRUE           '注入VNC并隐藏托盘
PE      SetBoot            1                              '下次本地启动
PE      Reboot                                            '重启
Sys     SetComputerName    Client-%ClientNum%             '改名
`

// WrapScript 规整脚本：去掉空行，保证每行都有合法前缀。
func WrapScript(body string) string {
	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "'") {
			out = append(out, line)
			continue
		}
		head := strings.ToUpper(strings.Fields(line)[0])
		if head != "ALL" && head != "PE" && head != "SYS" {
			line = "ALL " + strings.TrimLeft(line, " \t")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ScriptSummary 统计脚本里各类前缀数量，UI 上做个小提示。
func ScriptSummary(body string) string {
	n := map[string]int{"ALL": 0, "PE": 0, "SYS": 0, "SKIP": 0}
	for _, raw := range strings.Split(body, "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "'") {
			continue
		}
		head := strings.ToUpper(strings.Fields(s)[0])
		if head == "ALL" || head == "PE" || head == "SYS" {
			n[head]++
		} else {
			n["SKIP"]++
		}
	}
	return fmt.Sprintf("ALL %d / PE %d / SYS %d / 无前缀 %d",
		n["ALL"], n["PE"], n["SYS"], n["SKIP"])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
