package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// bit 构造盘符位图，例如 bit("C","D","X")
func bit(letters ...string) uint32 {
	var mask uint32
	for _, L := range letters {
		mask |= 1 << uint(L[0]-'A')
	}
	return mask
}

func TestDriveLettersToFree(t *testing.T) {
	// PE 里常见：C(旧系统) D(光驱) E(数据) 都被占，X 是 PE 自己的 ramdisk
	mask := bit("C", "D", "E", "X")
	got := driveLettersToFree(mask, "X", "X")
	want := []string{"C", "D", "E"}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Errorf("应腾出 C/D/E，实际 %v", got)
	}

	// A/B 软驱盘符永远不动
	if got := driveLettersToFree(bit("A", "B"), "X", "X"); len(got) != 0 {
		t.Errorf("A/B 不应被腾出: %v", got)
	}
	// 程序所在盘也要跳过（比如从 C:/ 运行）
	if got := driveLettersToFree(bit("C", "D"), "X", "C"); strings.Join(got, "") != "D" {
		t.Errorf("应跳过程序盘 C，实际 %v", got)
	}
}

func TestFreeLetterList(t *testing.T) {
	// C 被占、X 是系统盘 → 可用盘符应从 D 开始，且不含 X
	free := freeLetterList(bit("C", "X"), "X", "X")
	if len(free) == 0 || free[0] != "D" {
		t.Fatalf("首个可用盘符应为 D，实际 %v", free)
	}
	for _, L := range free {
		if L == "C" || L == "X" {
			t.Errorf("%s 不应出现在可用列表里: %v", L, free)
		}
	}
}

func TestRewriteAssignLetters(t *testing.T) {
	script := strings.Join([]string{
		"Select disk 0",
		"Clean",
		"convert gpt",
		"Create partition primary size=19456 align=2048",
		"select partition 3",
		"assign letter=C",
		"format quick fs=NTFS",
		"Create partition primary size=19456 align=2048",
		"select partition 4",
		"assign letter=D",
		"format quick fs=NTFS",
		"Create partition primary align=2048",
		"select partition 5",
		"assign letter=E",
		"format quick fs=NTFS",
		"select volume C",
		"label 系统",
		"select volume D",
		"label 软件",
		"select volume E",
		"label 文档",
		"exit",
	}, "\r\n")

	// D 抢不到，可用的是 C/F/G
	out, mapping := rewriteAssignLetters(script, []string{"C", "F", "G"})
	if mapping["D"] != "F" || mapping["E"] != "G" {
		t.Fatalf("映射不对: %v", mapping)
	}
	for _, want := range []string{"assign letter=C", "assign letter=F", "assign letter=G",
		"select volume C", "select volume F", "select volume G"} {
		if !strings.Contains(out, want) {
			t.Errorf("改写后缺少 %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "assign letter=D") || strings.Contains(out, "select volume D") {
		t.Errorf("旧的 D 没被替换掉:\n%s", out)
	}
	// 卷标行必须跟着盘符一起改，否则会打到别的分区上
	if !strings.Contains(out, "select volume F\r\nlabel 软件") {
		t.Errorf("卷标与盘符没对上:\n%s", out)
	}

	// 盘符本来就够用时，脚本应保持原样
	same, m2 := rewriteAssignLetters(script, []string{"C", "D", "E"})
	if same != script || len(m2) != 3 {
		t.Errorf("无需改写时脚本不应变化")
	}
}

func TestWantedLetters(t *testing.T) {
	script := "assign letter=C\r\nassign letter=D\r\nselect volume C\r\nassign letter=E\r\n"
	got := wantedLetters(script)
	if strings.Join(got, "") != "CDE" {
		t.Errorf("应解析出 C/D/E，实际 %v", got)
	}
	if got := wantedLetters("Clean\r\nexit"); len(got) != 0 {
		t.Errorf("没有 assign 时应返回空，实际 %v", got)
	}
}

func TestOccupiedAndFree(t *testing.T) {
	mask := bit("C", "D")
	if !occupied(mask, "C") || occupied(mask, "E") {
		t.Error("occupied 判断不对")
	}
	free := freeLetterList(mask, "X", "X")
	if free[0] != "E" {
		t.Errorf("C/D 被占后首个可用应为 E，实际 %v", free[:3])
	}
}

func TestWriteDiskpartScript(t *testing.T) {
	cli := New("127.0.0.1", 6003, true, "T", true, Callbacks{})
	e := NewEngine(cli, true)

	script := "Select disk 0\r\nClean\r\nlabel 系统\r\nexit\r\n"
	p, err := e.writeDiskpartScript(script)
	if err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	defer os.Remove(p)

	st, err := os.Stat(p)
	if err != nil || st.Size() == 0 {
		t.Fatalf("脚本文件不存在或为空: %v", err)
	}
	raw, _ := os.ReadFile(p)
	dec, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatalf("内容不是 GBK: %v", err)
	}
	if string(dec) != script {
		t.Errorf("写进去的内容被改动了\n got %q\nwant %q", string(dec), script)
	}
	t.Logf("脚本路径: %s（%d 字节）", p, st.Size())

	if _, err := e.writeDiskpartScript("   \r\n"); err == nil {
		t.Error("空脚本应当报错，而不是让 diskpart 去读空文件")
	}
}

func TestDiskpartOpenFailed(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"DiskPart 无法打开或读取脚本文件。", true},
		{"cannot open the script file", true},
		{"The system cannot find the file specified.", true},
		{"虚拟磁盘服务错误: 指定的驱动器号对于分配不可用。", false},
		{"DiskPart 已成功分配驱动器号。", false},
	}
	for _, c := range cases {
		if got := diskpartOpenFailed(c.out); got != c.want {
			t.Errorf("diskpartOpenFailed(%q)=%v, want %v", c.out, got, c.want)
		}
	}
}

// TestRunHandlesQuotedPath 回归测试：路径带空格时，Run 必须能把完整路径交给命令。
// 旧实现走 exec.Command("cmd","/c",...) 会把引号转义成 \" 被 cmd 吞掉，
// 导致 diskpart 报"无法打开脚本文件"。
func TestRunHandlesQuotedPath(t *testing.T) {
	cli := New("127.0.0.1", 6003, false, "T", true, Callbacks{})
	e := NewEngine(cli, false)
	dir := t.TempDir()
	f := filepath.Join(dir, "a b.txt")
	if err := os.WriteFile(f, []byte("hello-run"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out := e.Run(`type "`+f+`"`, true, 15)
	if rc != 0 || !strings.Contains(out, "hello-run") {
		t.Fatalf("Run 读带空格路径失败: rc=%d out=%q", rc, out)
	}
}

func TestAutoPartScheme(t *testing.T) {
	cases := []struct {
		gb    int
		count int
		sizes []int
	}{
		{120, 1, []int{0}},           // <256 → 1 分区（整盘）
		{255, 1, []int{0}},
		{256, 2, []int{200, 0}},      // 256~511 → 2 分区（首 200GB + 剩余）
		{511, 2, []int{200, 0}},
		{512, 3, []int{300, 106, 0}}, // ≥512 → 3 分区（首 300GB + 剩余两等分）
		{1024, 3, []int{300, 362, 0}},
		{2000, 3, []int{300, 850, 0}},
	}
	for _, c := range cases {
		n, sizes := autoPartScheme(c.gb)
		if n != c.count || len(sizes) != len(c.sizes) {
			t.Errorf("%dGB: got count=%d sizes=%v, want %d %v", c.gb, n, sizes, c.count, c.sizes)
			continue
		}
		for i := range sizes {
			if sizes[i] != c.sizes[i] {
				t.Errorf("%dGB: sizes[%d]=%d, want %d", c.gb, i, sizes[i], c.sizes[i])
			}
		}
	}
}
