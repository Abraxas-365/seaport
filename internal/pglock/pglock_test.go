package pglock_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/pglock"
)

func openDB(t *testing.T) *pglock.Locker {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	db, err := bootstrap.OpenDB(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { _ = db.Close() })
	return pglock.New(db.DB)
}

// Two lockers (two processes, each with its own pool) never hold one key
// at once; other keys don't wait; and many waiters of one key in a
// process use one connection (the pool of 3 doesn't run dry).
func TestLockAcrossProcesses(t *testing.T) {
	a, b := openDB(t), openDB(t)
	ctx := context.Background()
	var holders, maxHolders atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := a
			if i%2 == 1 {
				l = b
			}
			unlock, err := l.Lock(ctx, 1, "dock-1")
			if err != nil {
				t.Error(err)
				return
			}
			n := holders.Add(1)
			for {
				m := maxHolders.Load()
				if n <= m || maxHolders.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			holders.Add(-1)
			unlock()
		}()
	}
	// Meanwhile another key is free.
	unlock, err := a.Lock(ctx, 1, "dock-2")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	wg.Wait()
	if maxHolders.Load() != 1 {
		t.Fatalf("%d holders at once", maxHolders.Load())
	}
}

// A waiter whose context ends gives up (in the process queue or in
// Postgres) without leaking the lock or its connection.
func TestLockWaitCanceled(t *testing.T) {
	a, b := openDB(t), openDB(t)
	unlock, err := a.Lock(context.Background(), 2, "k")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []*pglock.Locker{a, b} { // queued locally / waiting in Postgres
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		if _, err := l.Lock(ctx, 2, "k"); err == nil {
			t.Fatal("got a held lock")
		}
		cancel()
	}
	unlock()
	for _, l := range []*pglock.Locker{b, a} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		u, err := l.Lock(ctx, 2, "k")
		cancel()
		if err != nil {
			t.Fatalf("lock after cancel: %v", err)
		}
		u()
	}
}
