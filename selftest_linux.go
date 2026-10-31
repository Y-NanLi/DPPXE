//go:build !windows

package main

import "fmt"

// testEngineRole Linux 版没有被控端，跳过脚本引擎自检。
//
// 打一行说明而不是静默跳过：否则用户会以为"自检项变少了是漏了什么"。
func testEngineRole(t *tester) {
	fmt.Println("[被控端脚本引擎] 跳过（Linux 版不含被控端）")
}
