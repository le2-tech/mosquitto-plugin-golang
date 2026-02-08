package main

import "strings"

// allowMessage 仅内置过滤系统主题：$SYS/#。
func allowMessage(topic string) (bool, string) {
	if topic == "$SYS" || strings.HasPrefix(topic, "$SYS/") {
		return false, "sys_topic"
	}
	return true, ""
}
