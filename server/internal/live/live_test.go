package live

import (
	"testing"
)

func kinds(es []Event) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

func TestSinceAnswersWhatWasPublishedAfterTheCursorInOrder(t *testing.T) {
	h := NewHub(8)
	start := h.Cursor()
	h.Publish(Event{Kind: WatchState, ItemID: 7})
	mid := h.Cursor()
	h.Publish(Event{Kind: Playback, SessionID: 3})
	h.Publish(Event{Kind: Station, StationID: 9})

	es, next, _, ok := h.Since(start)
	if !ok || len(es) != 3 || es[0].ItemID != 7 || es[1].SessionID != 3 || es[2].StationID != 9 {
		t.Fatalf("since start = %v ok=%v", kinds(es), ok)
	}
	if next != h.Cursor() {
		t.Fatalf("next = %q, want the hub's cursor %q", next, h.Cursor())
	}
	if es, _, _, ok := h.Since(mid); !ok || len(es) != 2 || es[0].Kind != Playback {
		t.Fatalf("since mid = %v ok=%v", kinds(es), ok)
	}
	if es, same, _, ok := h.Since(next); !ok || len(es) != 0 || same != next {
		t.Fatalf("since the end = %v %q ok=%v", kinds(es), same, ok)
	}
}

func TestSinceWakesOnTheNextPublish(t *testing.T) {
	h := NewHub(8)
	_, _, wait, _ := h.Since(h.Cursor())
	select {
	case <-wait:
		t.Fatal("woke with nothing published")
	default:
	}
	h.Publish(Event{Kind: Playlist})
	select {
	case <-wait:
	default:
		t.Fatal("a publish did not wake the waiter")
	}
}

func TestACursorTheBacklogNoLongerReachesMustResync(t *testing.T) {
	h := NewHub(2)
	old := h.Cursor()
	h.Publish(Event{Kind: Station})
	kept := h.Cursor()
	h.Publish(Event{Kind: Station})
	h.Publish(Event{Kind: Playlist})
	if _, _, _, ok := h.Since(old); ok {
		t.Fatal("a cursor three events back of a two-event backlog was continued")
	}
	if es, _, _, ok := h.Since(kept); !ok || len(es) != 2 {
		t.Fatalf("a cursor at the backlog's edge = %v ok=%v, want both events", kinds(es), ok)
	}
}

func TestACursorOfAnotherHubOrNoHubMustResync(t *testing.T) {
	a, b := NewHub(4), NewHub(4)
	a.Publish(Event{Kind: Station})
	for _, c := range []string{a.Cursor(), "", "junk", b.epoch + ".x", b.epoch + ".5", b.epoch + ".-1"} {
		if _, _, _, ok := b.Since(c); ok {
			t.Errorf("cursor %q was continued", c)
		}
	}
}

func TestANilHubDropsWhatIsPublished(t *testing.T) {
	var h *Hub
	h.Publish(Event{Kind: Station})
}

func TestHears(t *testing.T) {
	for _, c := range []struct {
		e                  Event
		household, account int64
		want               bool
	}{
		{Event{}, 1, 2, true},
		{Event{HouseholdID: 1}, 1, 2, true},
		{Event{HouseholdID: 3}, 1, 2, false},
		{Event{AccountIDs: []int64{5, 2}}, 1, 2, true},
		{Event{AccountIDs: []int64{5}}, 1, 2, false},
		{Event{HouseholdID: 1, AccountIDs: []int64{5}}, 1, 2, false},
	} {
		if got := c.e.Hears(c.household, c.account); got != c.want {
			t.Errorf("%+v hears(%d, %d) = %v", c.e, c.household, c.account, got)
		}
	}
}
