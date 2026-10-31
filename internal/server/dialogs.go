package server

import "strings"

// splitLines 把多行文本切成非空行（对话框返回的多选结果用）。
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
