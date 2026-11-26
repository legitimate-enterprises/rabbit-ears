package db_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

func TestNilPointerArgsBindNull(t *testing.T) {
	d := open(t)
	if _, err := d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER)`); err != nil {
		t.Fatal(err)
	}
	a, b := int64(7), 9
	for id, args := range [][2]any{{(*int64)(nil), (*int)(nil)}, {&a, &b}} {
		if _, err := d.Exec(`INSERT INTO t VALUES (?, ?, ?)`, id, args[0], args[1]); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range [][2]sql.NullInt64{{}, {{Int64: 7, Valid: true}, {Int64: 9, Valid: true}}} {
		var got [2]sql.NullInt64
		if err := d.QueryRow(`SELECT a, b FROM t WHERE id = ?`, id).Scan(&got[0], &got[1]); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row %d = %v, want %v", id, got, want)
		}
	}
}

func counter(t *testing.T) *sql.DB {
	t.Helper()
	d := open(t)
	if _, err := d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return d
}

// A read still streaming its rows holds a connection; another read does
// not wait for it.
func TestAReadDoesNotWaitOnAnotherRead(t *testing.T) {
	d := counter(t)
	for i := range 3 {
		if _, err := d.Exec(`INSERT INTO t (n) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	slow, err := d.Query(`SELECT n FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Close() }()
	if !slow.Next() {
		t.Fatal(slow.Err())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("a second read: %d %v", n, err)
	}
}

// Writers on several connections, alone or in transactions, wait their
// turn rather than fail.
func TestConcurrentWritesAllSucceed(t *testing.T) {
	d := counter(t)
	const writers, each = 16, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				if (w+i)%2 == 0 {
					_, err := d.Exec(`INSERT INTO t (n) VALUES (?)`, i)
					errs <- err
					continue
				}
				errs <- db.InTx(d, func(tx db.DBTX) error {
					var n int
					if err := tx.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.Exec(`INSERT INTO t (n) VALUES (?)`, n)
					return err
				})
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != writers*each {
		t.Fatalf("%d rows, want %d: %v", n, writers*each, err)
	}
}

// A transaction that reads and then writes succeeds although another
// connection tries to write between its read and its write; that write
// waits for the transaction and then succeeds too.
func TestAReadThenWriteTransactionRacingAWriterSucceeds(t *testing.T) {
	d := counter(t)
	if _, err := d.Exec(`INSERT INTO t (id, n) VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	other := make(chan error, 1)
	err := db.InTx(d, func(tx db.DBTX) error {
		var n int
		if err := tx.QueryRow(`SELECT n FROM t WHERE id = 1`).Scan(&n); err != nil {
			return err
		}
		go func() {
			_, err := d.Exec(`UPDATE t SET n = n + 10 WHERE id = 1`)
			other <- err
		}()
		// Long enough for the other write to commit, were it let through.
		select {
		case err := <-other:
			other <- err
		case <-time.After(200 * time.Millisecond):
		}
		_, err := tx.Exec(`UPDATE t SET n = ? WHERE id = 1`, n+1)
		return err
	})
	if err != nil {
		t.Fatalf("the transaction: %v", err)
	}
	if err := <-other; err != nil {
		t.Fatalf("the other write: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT n FROM t WHERE id = 1`).Scan(&n); err != nil || n != 11 {
		t.Fatalf("n = %d, want both writes: %v", n, err)
	}
}

// Every connection to ":memory:" is a database of its own, so it keeps
// one.
func TestAMemoryDatabaseKeepsOneConnection(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if n := d.Stats().MaxOpenConnections; n != 1 {
		t.Fatalf("%d connections", n)
	}
}

func TestFromNullInt(t *testing.T) {
	if p := db.FromNullInt(sql.NullInt64{}); p != nil {
		t.Errorf("NULL = %d, want nil", *p)
	}
	if p := db.FromNullInt(sql.NullInt64{Int64: 7, Valid: true}); p == nil || *p != 7 {
		t.Errorf("7 = %v, want 7", p)
	}
}

func TestColumnCollectsOneColumnRows(t *testing.T) {
	d := open(t)
	if _, err := d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO t VALUES (3, 'c'), (1, 'a')`); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`SELECT id FROM t ORDER BY id`)
	ids, err := db.Collect(rows, err, db.Column[int64])
	if err != nil || len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Errorf("ids = %v, %v; want [1 3]", ids, err)
	}
	rows, err = d.Query(`SELECT name FROM t ORDER BY id`)
	names, err := db.Collect(rows, err, db.Column[string])
	if err != nil || len(names) != 2 || names[0] != "a" || names[1] != "c" {
		t.Errorf("names = %v, %v; want [a c]", names, err)
	}
}
