package httpapi

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

// A change saved while another is between its read and its write applies
// to what that one saved, not to what both read.
func TestWatchStateChangesDoNotLoseEachOther(t *testing.T) {
	svc := service.New(dbtest.Open(t))
	s := &Server{svc: svc}
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A", PasswordHash: "x"}
	l := &library.Library{Name: "m", Kind: library.Movies}
	if err := svc.Accounts.Repo().Create(a); err != nil {
		t.Fatal(err)
	}
	if err := svc.Libraries.Repo().Create(l); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "m"}
	if err := svc.Items.Repo().Create(it); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := s.saveWatchState(a.ID, it.ID, func(w *watchstate.State) {
			close(entered)
			time.Sleep(200 * time.Millisecond) // long enough for the second to save, were it let through
			w.PlayCount++
		})
		first <- err
	}()
	<-entered
	if _, err := s.saveWatchState(a.ID, it.ID, func(w *watchstate.State) { w.PlayCount++ }); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	w, err := svc.WatchStates.Repo().Get(a.ID, it.ID)
	if err != nil || w.PlayCount != 2 {
		t.Fatalf("play count %d, want both changes: %v", w.PlayCount, err)
	}
}
