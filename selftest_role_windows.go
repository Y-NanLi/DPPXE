//go:build windows

package main

// testEngineRole Windows 上有被控端脚本引擎，走完整自检。
func testEngineRole(t *tester) { testEngine(t) }
