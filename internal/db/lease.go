package db

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lease reserves one connection and one admission slot across multiple transactions.
// It deliberately has no raw Conn accessor. Release discards the connection so
// session advisory locks and session settings cannot leak into the next caller.
type Lease struct {
	raw     *pgxpool.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	release func()
	mu      sync.Mutex
	closed  bool
	stop    func() bool
}

// AcquireBudgeted reserves capacity for the complete lease lifetime without
// changing the legacy pgxpool Acquire API. Cancellation and Close discard it.
func (p *Pool) AcquireBudgeted(ctx context.Context) (*Lease, error) {
	op, release, err := p.enter(ctx)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(op)
	acquire := life
	stopAcquire := func() {}
	if p.admission != nil {
		var cancelAcquire context.CancelFunc
		acquire, cancelAcquire = context.WithTimeout(life, p.admission.budget.AcquireTimeout)
		stopAcquire = cancelAcquire
	}
	raw, err := p.Acquire(acquire)
	stopAcquire()
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	l := &Lease{raw: raw, ctx: life, cancel: cancel, release: release}
	l.mu.Lock()
	l.stop = context.AfterFunc(life, l.Release)
	l.mu.Unlock()
	return l, nil
}

// Release is idempotent and cancels outstanding operations before closing the
// physical connection, ensuring PostgreSQL releases all session advisory locks.
func (l *Lease) Release() {
	l.cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.stop != nil {
		l.stop()
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = l.raw.Conn().Close(cleanup)
	l.raw.Release()
	l.release()
}

// Context carries the lease lifetime deadline and cancellation to external work.
func (l *Lease) Context() context.Context { return l.ctx }

func (l *Lease) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return (&budgetTx{ctx: l.ctx}).operationContext(ctx)
}

// Exec executes a statement while retaining the lease reservation.
func (l *Lease) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return pgconn.CommandTag{}, ErrPoolClosed
	}
	op, cancel := l.operationContext(ctx)
	defer cancel()
	return l.raw.Exec(op, sql, args...)
}

// Query retains exclusive connection use until the rows finish or are canceled.
func (l *Lease) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrPoolClosed
	}
	op, cancel := l.operationContext(ctx)
	rows, err := l.raw.Query(op, sql, args...)
	if err != nil {
		cancel()
		l.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return newBudgetRows(op, rows, func() { once.Do(func() { cancel(); l.mu.Unlock() }) }), nil
}

// QueryRow retains connection use through Scan or cancellation.
func (l *Lease) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := l.Query(ctx, sql, args...)
	return &budgetRow{rows: rows, err: err}
}

// Begin starts a transaction on this lease, retaining its admission reservation
// after Commit or Rollback so callers can safely perform a second transaction.
func (l *Lease) Begin(ctx context.Context) (pgx.Tx, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrPoolClosed
	}
	op, cancel := l.operationContext(ctx)
	tx, err := l.raw.Begin(op)
	if err != nil {
		cancel()
		l.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	wrapped := &budgetTx{Tx: tx, ctx: op, release: func() { once.Do(func() { cancel(); l.mu.Unlock() }) }}
	wrapped.mu.Lock()
	wrapped.stop = context.AfterFunc(op, func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = wrapped.Rollback(cleanup)
	})
	wrapped.mu.Unlock()
	return wrapped, nil
}
