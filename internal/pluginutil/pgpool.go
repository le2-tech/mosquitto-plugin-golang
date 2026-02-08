package pluginutil

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
) (*pgxpool.Pool, error) {
	mu.RLock()
	if *holder != nil {
		p := *holder
		mu.RUnlock()
		return p, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if *holder != nil {
		return *holder, nil
	}

	p, err := NewDefaultPGPool(ctx, dsn)
	if err != nil {
		return nil, err
	}
	*holder = p
	log.Printf("postgres pool connected dsn=%s", SafeDSN(dsn))
	return p, nil
}
