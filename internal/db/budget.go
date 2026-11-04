package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Budget bounds active operations separately from queued callers. Queue excludes
// active operations. Optional work must use Pool methods, not the raw Pool field.
type Budget struct {
	MaxConns         int32
	MaxQueue         int
	AcquireTimeout   time.Duration
	OperationTimeout time.Duration
}

// ErrBudgetExhausted reports that no queue slot is available.
var ErrBudgetExhausted = errors.New("database work queue full")

// ErrPoolClosed reports that shutdown has begun.
var ErrPoolClosed = errors.New("database pool closed")

type admission struct {
	active chan struct{}
	queued chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	budget Budget
}

func newAdmission(b Budget) *admission {
	ctx, cancel := context.WithCancel(context.Background())
	return &admission{active: make(chan struct{}, b.MaxConns), queued: make(chan struct{}, b.MaxQueue), ctx: ctx, cancel: cancel, budget: b}
}

func (a *admission) enter(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if a.ctx.Err() != nil {
		return nil, nil, ErrPoolClosed
	}
	select {
	case a.active <- struct{}{}:
	default:
		select {
		case a.queued <- struct{}{}:
		default:
			return nil, nil, ErrBudgetExhausted
		}
		timer := time.NewTimer(a.budget.AcquireTimeout)
		defer timer.Stop()
		select {
		case a.active <- struct{}{}:
		case <-ctx.Done():
			<-a.queued
			return nil, nil, ctx.Err()
		case <-a.ctx.Done():
			<-a.queued
			return nil, nil, ErrPoolClosed
		case <-timer.C:
			<-a.queued
			return nil, nil, context.DeadlineExceeded
		}
		<-a.queued
	}
	op, cancel := context.WithTimeout(ctx, a.budget.OperationTimeout)
	stop := context.AfterFunc(a.ctx, cancel)
	var once sync.Once
	release := func() { once.Do(func() { stop(); cancel(); <-a.active }) }
	if a.ctx.Err() != nil {
		release()
		return nil, nil, ErrPoolClosed
	}
	return op, release, nil
}

// OpenBudgeted opens another physical pool against the supplied authoritative
// database URL. It never accepts a separate optional-service database URL.
func OpenBudgeted(ctx context.Context, url, role, name string, b Budget) (*Pool, error) {
	if b.MaxConns < 1 || b.MaxQueue < 0 || b.MaxQueue > 1024 || b.AcquireTimeout <= 0 || b.OperationTimeout < time.Millisecond {
		return nil, fmt.Errorf("invalid database budget")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = b.MaxConns
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = b.AcquireTimeout
	cfg.ConnConfig.RuntimeParams["role"] = role
	cfg.ConnConfig.RuntimeParams["application_name"] = "urmaild-" + name
	timeout := strconv.FormatInt(b.OperationTimeout.Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = timeout
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = timeout
	raw, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p := &Pool{Pool: raw, role: role, admission: newAdmission(b)}
	pingCtx, cancel := context.WithTimeout(ctx, b.AcquireTimeout)
	defer cancel()
	if err := raw.Ping(pingCtx); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Close cancels budgeted work and closes all physical connections.
func (p *Pool) Close() {
	if p.admission != nil {
		p.admission.cancel()
	}
	p.Pool.Close()
}

func (p *Pool) enter(ctx context.Context) (context.Context, func(), error) {
	if p.admission == nil {
		return ctx, func() {}, nil
	}
	return p.admission.enter(ctx)
}

// Exec executes one statement within the configured operation budget.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	op, release, err := p.enter(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer release()
	return p.Pool.Exec(op, sql, args...)
}

// Query reserves capacity until rows close, finish, or reach their deadline.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if p.admission == nil {
		return p.Pool.Query(ctx, sql, args...)
	}
	op, release, err := p.enter(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := p.Pool.Query(op, sql, args...)
	if err != nil {
		release()
		return nil, err
	}
	return newBudgetRows(op, rows, release), nil
}

type budgetRows struct {
	pgx.Rows
	release func()
	mu      sync.Mutex
	stop    func() bool
}

func newBudgetRows(ctx context.Context, rows pgx.Rows, release func()) *budgetRows {
	r := &budgetRows{Rows: rows, release: release}
	r.mu.Lock()
	r.stop = context.AfterFunc(ctx, r.Close)
	r.mu.Unlock()
	return r
}
func (r *budgetRows) closeLocked() {
	r.Rows.Close()
	if r.stop != nil {
		r.stop()
	}
	r.release()
}
func (r *budgetRows) Close() { r.mu.Lock(); defer r.mu.Unlock(); r.closeLocked() }
func (r *budgetRows) Next() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ok := r.Rows.Next()
	if !ok {
		r.closeLocked()
	}
	return ok
}
func (r *budgetRows) Scan(dest ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Rows.Scan(dest...)
}
func (r *budgetRows) Values() ([]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Rows.Values()
}
func (r *budgetRows) RawValues() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw := r.Rows.RawValues()
	copyValues := make([][]byte, len(raw))
	for i, v := range raw {
		if v != nil {
			copyValues[i] = append([]byte{}, v...)
		}
	}
	return copyValues
}
func (r *budgetRows) Err() error { r.mu.Lock(); defer r.mu.Unlock(); return r.Rows.Err() }
func (r *budgetRows) CommandTag() pgconn.CommandTag {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Rows.CommandTag()
}

// QueryRow reserves capacity until Scan or the operation deadline.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if p.admission == nil {
		return p.Pool.QueryRow(ctx, sql, args...)
	}
	rows, err := p.Query(ctx, sql, args...)
	return &budgetRow{rows: rows, err: err}
}

type budgetRow struct {
	rows pgx.Rows
	err  error
}

func (r *budgetRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}

// Begin starts a transaction with bounded lifetime for budgeted pools.
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) { return p.BeginTx(ctx, pgx.TxOptions{}) }

// BeginTx starts a transaction and retains capacity through its terminal operation.
func (p *Pool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if p.admission == nil {
		return p.Pool.BeginTx(ctx, opts)
	}
	op, release, err := p.enter(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := p.Pool.BeginTx(op, opts)
	if err != nil {
		release()
		return nil, err
	}
	wrapped := &budgetTx{Tx: tx, release: release, ctx: op}
	wrapped.mu.Lock()
	wrapped.stop = context.AfterFunc(op, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = wrapped.Rollback(cleanup)
	})
	wrapped.mu.Unlock()
	return wrapped, nil
}

type budgetTx struct {
	pgx.Tx
	release func()
	ctx     context.Context
	mu      sync.Mutex
	stop    func() bool
}

func (t *budgetTx) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, bounded := t.ctx.Deadline()
	var op context.Context
	var cancel context.CancelFunc
	if bounded {
		op, cancel = context.WithDeadline(ctx, deadline)
	} else {
		op, cancel = context.WithCancel(ctx)
	}
	stop := context.AfterFunc(t.ctx, cancel)
	return op, func() { stop(); cancel() }
}
func (t *budgetTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	op, cancel := t.operationContext(ctx)
	defer cancel()
	return t.Tx.Exec(op, sql, args...)
}
func (t *budgetTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	t.mu.Lock()
	op, cancel := t.operationContext(ctx)
	rows, err := t.Tx.Query(op, sql, args...)
	if err != nil {
		cancel()
		t.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return newBudgetRows(op, rows, func() { once.Do(func() { cancel(); t.mu.Unlock() }) }), nil
}
func (t *budgetTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := t.Query(ctx, sql, args...)
	return &budgetRow{rows: rows, err: err}
}
func (t *budgetTx) Commit(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stop != nil {
		t.stop()
	}
	defer t.release()
	op, cancel := t.operationContext(ctx)
	defer cancel()
	return t.Tx.Commit(op)
}
func (t *budgetTx) Rollback(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stop != nil {
		t.stop()
	}
	defer t.release()
	return t.Tx.Rollback(ctx)
}
