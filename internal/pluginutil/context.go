package pluginutil

import (
	"context"
	"time"
)

// TimeoutContext 根据超时值创建 context；当 timeout<=0 时返回 Background。
func TimeoutContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), timeout)
}
