package cqrs_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"starbase/internal/cqrs"
	"starbase/internal/db"
)

type insertUser struct{ login string }

func (c insertUser) Apply(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO users(github_id, login, created_at) VALUES (?, ?, 0)`, len(c.login), c.login)
	return err
}

type scopedNoop struct{ sid string }

func (scopedNoop) Apply(context.Context, *sql.Tx) error { return nil }
func (c scopedNoop) Scope() string                      { return c.sid }

type failing struct{}

func (failing) Apply(context.Context, *sql.Tx) error { return errors.New("boom") }

// panicking writes its row, then panics.
type panicking struct{ login string }

func (c panicking) Apply(ctx context.Context, tx *sql.Tx) error {
	if err := (insertUser{c.login}).Apply(ctx, tx); err != nil {
		return err
	}
	panic("boom")
}

type invalid struct{}

func (invalid) Apply(context.Context, *sql.Tx) error { return nil }
func (invalid) Validate() error                      { return errors.New("nope") }

func setup(t *testing.T) (*db.DB, *cqrs.Hub, *cqrs.Bus) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	hub := cqrs.NewHub()
	bus := cqrs.NewBus(d.W, hub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go bus.Run(ctx)
	return d, hub, bus
}

func TestFailingCommandDoesNotAbortBatch(t *testing.T) {
	d, _, bus := setup(t)
	ctx := context.Background()
	bus.Send(insertUser{"a"})
	if err := bus.Exec(ctx, failing{}); err == nil {
		t.Fatal("expected error")
	}
	if err := bus.Exec(ctx, insertUser{"bb"}); err != nil {
		t.Fatal(err)
	}
	var n int
	d.R.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}
}

func TestPanickingCommandDoesNotAbortBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	var log bytes.Buffer
	bus := cqrs.NewBus(d.W, cqrs.NewHub(), slog.New(slog.NewTextHandler(&log, nil)))
	// Queued before the bus runs, so the three share one batch.
	bus.Send(insertUser{"a"})
	bus.Send(panicking{"bb"})
	bus.Send(insertUser{"ccc"})
	go bus.Run(ctx)
	if err := bus.Exec(ctx, panicking{"dddd"}); err == nil {
		t.Fatal("expected the panic as the command's error")
	}
	if err := bus.Exec(ctx, insertUser{"eeeee"}); err != nil {
		t.Fatal(err)
	}
	var logins string
	if err := d.R.QueryRow(`SELECT group_concat(login, ' ' ORDER BY login) FROM users`).Scan(&logins); err != nil {
		t.Fatal(err)
	}
	if want := "a ccc eeeee"; logins != want {
		t.Fatalf("users = %q, want %q", logins, want)
	}
	if !strings.Contains(log.String(), "panicking.Apply") {
		t.Fatalf("panic logged without its stack:\n%s", log.String())
	}
}

func TestValidation(t *testing.T) {
	_, _, bus := setup(t)
	if err := bus.Send(invalid{}); !errors.Is(err, cqrs.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestScopedNotify(t *testing.T) {
	_, hub, bus := setup(t)
	mine := hub.Subscribe("s1", "t1", "/")
	other := hub.Subscribe("s2", "t1", "/other")
	if err := bus.Exec(context.Background(), scopedNoop{"s1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mine.C:
	case <-time.After(time.Second):
		t.Fatal("scoped stream not woken")
	}
	select {
	case <-other.C:
		t.Fatal("unrelated stream woken")
	default:
	}
	// Unscoped commands wake everyone.
	bus.Exec(context.Background(), insertUser{"c"})
	for _, s := range []*cqrs.Sub{mine, other} {
		select {
		case <-s.C:
		case <-time.After(time.Second):
			t.Fatal("stream not woken by broadcast")
		}
	}
}

func TestNotifyCoalesces(t *testing.T) {
	hub := cqrs.NewHub()
	s := hub.Subscribe("s", "t", "/")
	for range 10 {
		hub.NotifyAll()
	}
	<-s.C
	select {
	case <-s.C:
		t.Fatal("expected a single pending wake-up")
	default:
	}
}

func TestPresence(t *testing.T) {
	hub := cqrs.NewHub()
	a := hub.Subscribe("s1", "t1", "/showcase")
	if hub.Count("/showcase") != 1 || hub.Count("/") != 0 {
		t.Fatal("counts")
	}
	b := hub.Subscribe("s2", "t2", "/showcase")
	select {
	case <-a.C: // woken: someone joined
	default:
		t.Fatal("existing viewer not woken on join")
	}
	hub.Unsubscribe(b)
	select {
	case <-a.C:
	default:
		t.Fatal("existing viewer not woken on leave")
	}
	if hub.Count("/showcase") != 1 {
		t.Fatal("count after leave")
	}
}
