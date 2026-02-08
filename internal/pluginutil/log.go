package pluginutil

import (
	"fmt"
	"sort"
	"strings"
)

// FormatLogMessage 统一格式化日志为 "msg k=v ..." 形式，并按 key 排序保证输出稳定。
func FormatLogMessage(msg string, fields ...map[string]any) string {
	var b strings.Builder
	b.WriteString(msg)
	if len(fields) == 0 || fields[0] == nil {
		return b.String()
	}

	keys := make([]string, 0, len(fields[0]))
	for k := range fields[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, fields[0][k])
	}
	return b.String()
}
