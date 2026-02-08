package main

import "strings"

// parseFailMode 解析失败处理策略。
func parseFailMode(v string) (failMode, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "drop":
		return failModeDrop, true
	case "block":
		return failModeBlock, true
	case "disconnect":
		return failModeDisconnect, true
	default:
		return failModeDrop, false
	}
}

// failModeString 将失败策略转回配置字符串。
func failModeString(mode failMode) string {
	switch mode {
	case failModeDrop:
		return "drop"
	case failModeBlock:
		return "block"
	case failModeDisconnect:
		return "disconnect"
	default:
		return "drop"
	}
}
