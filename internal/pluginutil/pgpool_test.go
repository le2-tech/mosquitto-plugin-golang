package pluginutil

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureSharedPGPoolExisting(t *testing.T) {
	mu := &sync.RWMutex{}
	existing := &pgxpool.Pool{}
	holder := existing

	oldFactory := newDefaultPGPool
	t.Cleanup(func() { newDefaultPGPool = oldFactory })
	newDefaultPGPool = func(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
		t.Fatal("factory should not be called when holder already exists")
		return nil, nil
	}

	p, ev, err := EnsureSharedPGPool(context.Background(), mu, &holder, "postgres://x")
	if err != nil {
		t.Fatalf("EnsureSharedPGPool returned error: %v", err)
	}
	if p != existing {
		t.Fatalf("returned pool mismatch: got %p want %p", p, existing)
	}
	if ev != PGPoolEventNone {
		t.Fatalf("event mismatch: got %v want %v", ev, PGPoolEventNone)
	}
}

func TestEnsureSharedPGPoolCreates(t *testing.T) {
	mu := &sync.RWMutex{}
	var holder *pgxpool.Pool
	created := &pgxpool.Pool{}

	oldFactory := newDefaultPGPool
	t.Cleanup(func() { newDefaultPGPool = oldFactory })
	newDefaultPGPool = func(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
		return created, nil
	}

	p, ev, err := EnsureSharedPGPool(context.Background(), mu, &holder, "postgres://x")
	if err != nil {
		t.Fatalf("EnsureSharedPGPool returned error: %v", err)
	}
	if p != created || holder != created {
		t.Fatalf("created pool mismatch: got p=%p holder=%p want=%p", p, holder, created)
	}
	if ev != PGPoolEventConnected {
		t.Fatalf("event mismatch: got %v want %v", ev, PGPoolEventConnected)
	}
}

func TestEnsureSharedPGPoolCreateError(t *testing.T) {
	mu := &sync.RWMutex{}
	var holder *pgxpool.Pool
	wantErr := errors.New("dial failed")

	oldFactory := newDefaultPGPool
	t.Cleanup(func() { newDefaultPGPool = oldFactory })
	newDefaultPGPool = func(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
		return nil, wantErr
	}

	p, ev, err := EnsureSharedPGPool(context.Background(), mu, &holder, "postgres://x")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error mismatch: got %v want %v", err, wantErr)
	}
	if p != nil || holder != nil {
		t.Fatalf("pool should remain nil on error: p=%p holder=%p", p, holder)
	}
	if ev != PGPoolEventNone {
		t.Fatalf("event mismatch: got %v want %v", ev, PGPoolEventNone)
	}
}
