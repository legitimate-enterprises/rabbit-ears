// Package live holds the event stream clients follow to learn that what
// they show has changed. An event names a kind and ids only: a client reads
// what changed through the API, which decides what its profile may see.
package live

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Kinds of event.
const (
	WatchState    = "watch_state"
	Playback      = "playback"
	Station       = "station"
	WatchTogether = "watch_together"
	Playlist      = "playlist"
	Library       = "library"
	Household     = "household"
)

// What happened, for the kinds that say.
const (
	Started = "started"
	Moved   = "moved"
	Ended   = "ended"
	Opened  = "opened"
	Joined  = "joined"
)

// Event is one change. HouseholdID and AccountIDs say who hears it: 0 is
// every household, none every profile. OpenerID, when not 0, is also heard
// by the profiles who see that account, which the reader decides. The
// other ids are 0 when the event has none.
type Event struct {
	Kind        string
	Change      string
	HouseholdID int64
	AccountIDs  []int64
	OpenerID    int64

	ItemID          int64
	SessionID       int64
	DeviceID        int64
	StationID       int64
	PlaylistID      int64
	WatchTogetherID int64
	LibraryID       int64
}

// Hears reports whether a profile of a household is among those e is for.
func (e Event) Hears(householdID, accountID int64) bool {
	return (e.HouseholdID == 0 || e.HouseholdID == householdID) && (len(e.AccountIDs) == 0 || slices.Contains(e.AccountIDs, accountID))
}

// Hub keeps the latest events in memory, at most its size of them, for
// every follower to read from its own cursor. A cursor names the hub that
// made it, so one from before a restart is never continued.
type Hub struct {
	mu      sync.Mutex
	epoch   string
	seq     int64 // of the newest event
	backlog []Event
	size    int
	changed chan struct{}
}

// NewHub returns a hub that keeps the last size events.
func NewHub(size int) *Hub {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return &Hub{epoch: hex.EncodeToString(b), size: size, changed: make(chan struct{})}
}

// Publish adds e and wakes every follower waiting. A nil hub drops it.
func (h *Hub) Publish(e Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	h.backlog = append(h.backlog, e)
	if len(h.backlog) > h.size {
		h.backlog = h.backlog[len(h.backlog)-h.size:]
	}
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *Hub) cursor() string { return h.epoch + "." + strconv.FormatInt(h.seq, 10) }

// Cursor is the place after the newest event.
func (h *Hub) Cursor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor()
}

// Since returns the events after cursor, oldest first, the cursor after
// them and a channel closed by the next Publish. ok is false for a cursor
// the hub cannot continue from: another hub's, or one further back than
// the backlog reaches. The follower then starts over from next.
func (h *Hub) Since(cursor string) (events []Event, next string, wait <-chan struct{}, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	epoch, n, found := strings.Cut(cursor, ".")
	seq, err := strconv.ParseInt(n, 10, 64)
	behind := h.seq - seq
	if !found || err != nil || epoch != h.epoch || behind < 0 || behind > int64(len(h.backlog)) {
		return nil, h.cursor(), h.changed, false
	}
	return slices.Clone(h.backlog[int64(len(h.backlog))-behind:]), h.cursor(), h.changed, true
}
