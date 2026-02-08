package pluginutil

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256PwdSalt 使用盐对密码做 SHA-256，并返回十六进制字符串。
func SHA256PwdSalt(password, salt string) string {
	sum := sha256.Sum256([]byte(password + salt))
	return hex.EncodeToString(sum[:])
}
