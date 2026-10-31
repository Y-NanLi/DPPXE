//go:build windows

package main

import (
	"fmt"
	"strings"

	"dppxe/internal/client"
)

// testEngine 被控端脚本引擎自检。
//
// 这一段只测 Windows 被控端（engine.go 里的 diskpart / wimlib 指令解析），
// 所以只在 Windows 构建里跑；Linux 版没有对应实现，见 selftest_linux.go。
func testEngine(t *tester) {
	fmt.Println("[被控端脚本引擎]")
	var lines []string
	cb := client.Callbacks{OnLog: func(s string) { lines = append(lines, s) }}
	c := client.New("127.0.0.1", 6003, true, "SELFTEST", true, cb)
	eng := client.NewEngine(c, true)

	script := `ALL ADDLOG 开始 %COMPUTERNAME%
ALL SETVAR X=5
ALL IF %X% = 5 THEN ADDLOG 条件成立
ALL FOR I=1 TO 3
ALL ADDLOG 循环 %I%
ALL NEXT
:END
ALL ADDLOG 结束`
	eng.RunScript(script)
	joined := strings.Join(lines, "|")
	t.check("变量替换 + 单行 IF", strings.Contains(joined, "条件成立"), joined)
	t.check("FOR/NEXT 循环 3 次",
		strings.Contains(joined, "循环 1") && strings.Contains(joined, "循环 3") &&
			!strings.Contains(joined, "循环 4"), joined)
	t.check("ADDLOG 执行", strings.Contains(joined, "结束"), joined)

	lines = nil
	eng.ExecRaw(`UNCOMPRESS "D:\a.wim|2" C: TRUE`)
	t.check("结构化指令 UNCOMPRESS 解析",
		strings.Contains(strings.Join(lines, "|"), "卷2"), strings.Join(lines, "|"))

	lines = nil
	eng.ExecRaw("SETVAR P=42")
	eng.ExecRaw("ADDLOG 值是 %P%")
	t.check("SETVAR 变量生效", strings.Contains(strings.Join(lines, "|"), "值是 42"),
		strings.Join(lines, "|"))
}
