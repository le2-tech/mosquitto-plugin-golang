package pluginutil

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGPoolEvent 表示 EnsureSharedPGPool 调用期间发生的池状态事件。
type PGPoolEvent int

const (
	PGPoolEventNone PGPoolEvent = iota
	PGPoolEventConnected
)

var newDefaultPGPool = NewDefaultPGPool

// NewDefaultPGPool 使用统一默认参数创建并探活 PostgreSQL 连接池。
func NewDefaultPGPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 60 * time.Second
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// EnsureSharedPGPool 在受互斥锁保护的共享指针上执行单次连接池初始化。
func EnsureSharedPGPool(
	ctx context.Context,
	mu *sync.RWMutex,
	holder **pgxpool.Pool,
	dsn string,
) (*pgxpool.Pool, PGPoolEvent, error) {
	mu.RLock()
	if *holder != nil {
		p := *holder
		mu.RUnlock()
		return p, PGPoolEventNone, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if *holder != nil {
		return *holder, PGPoolEventNone, nil
	}

	p, err := newDefaultPGPool(ctx, dsn)
	if err != nil {
		return nil, PGPoolEventNone, err
	}
	*holder = p
	return p, PGPoolEventConnected, nil
}
