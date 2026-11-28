package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) sendFriend(token, email string) (int, []byte) {
	e.t.Helper()
	return e.do("POST", "/friend-requests", token, map[string]any{"email": email}, nil)
}

func (e *env) friendRequests(token string) api.FriendRequestList {
	e.t.Helper()
	var l api.FriendRequestList
	e.expect(200, "GET", "/friend-requests", token, nil, &l)
	return l
}

// befriend has from ask to's email and to accept.
func (e *env) befriend(from, to user) {
	e.t.Helper()
	e.expect(202, "POST", "/friend-requests", from.token, map[string]any{"email": to.account.Email}, nil)
	in := e.friendRequests(to.token).Incoming
	i := slices.IndexFunc(in, func(r api.IncomingFriendRequest) bool { return r.FromProfileId == from.account.ID })
	if i < 0 {
		e.t.Fatalf("%s did not receive %s's request: %+v", to.account.Email, from.account.Email, in)
	}
	e.expect(204, "POST", "/friend-requests/"+itoa(in[i].Id)+"/accept", to.token, nil, nil)
}

func (e *env) friends(token string) []uid.ID {
	e.t.Helper()
	var page api.AccountPage
	e.expect(200, "GET", "/friends", token, nil, &page)
	out := []uid.ID{}
	for _, a := range page.Items {
		if a.Email != nil || a.HouseholdId != nil {
			e.t.Fatalf("a friend shows their email or household: %+v", a)
		}
		out = append(out, a.Id)
	}
	return out
}

// replicaRows is every row of token's household's replica, each as its
// table and values.
func (e *env) replicaRows(token string) []string {
	e.t.Helper()
	body, _, _ := e.snapshot(token)
	p := filepath.Join(e.t.TempDir(), "rows.sqlite")
	must(e.t, os.WriteFile(p, body, 0o600))
	r, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
	must(e.t, err)
	defer func() { _ = r.Close() }()
	rows, err := r.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name <> 'replica_meta' ORDER BY name`)
	must(e.t, err)
	var tables []string
	for rows.Next() {
		var n string
		must(e.t, rows.Scan(&n))
		tables = append(tables, n)
	}
	must(e.t, rows.Close())
	var out []string
	for _, tb := range tables {
		rs, err := r.Query(`SELECT * FROM "` + tb + `"`) //nolint:gosec // tb is a table name the snapshot's schema listed
		must(e.t, err)
		cols, err := rs.Columns()
		must(e.t, err)
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			must(e.t, rs.Scan(ptrs...))
			out = append(out, tb+":"+fmt.Sprint(vals...))
		}
		must(e.t, rs.Close())
	}
	slices.Sort(out)
	return out
}

func minus(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// sent is a sendFriendRequest answer with what differs from one request to
// the next taken out: its id, its instants and the email as written.
func sent(t *testing.T, status int, raw []byte, email string) string {
	t.Helper()
	var r api.OutgoingFriendRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%d %s", status, raw)
	}
	var m map[string]any
	must(t, json.Unmarshal(raw, &m))
	if r.Email != strings.TrimSpace(email) || r.ExpiresAt.Sub(r.CreatedAt) != service.FriendRequestLife || r.Id.IsZero() {
		t.Fatalf("not the request as sent: %s", raw)
	}
	m["id"], m["email"], m["createdAt"], m["expiresAt"] = "ID", "EMAIL", "T", "T"
	b, err := json.Marshal(m)
	must(t, err)
	return fmt.Sprintf("%d %s", status, b)
}

// Whoever holds the email, and whatever they did with an earlier request,
// a sender sees the same: the same answer to the request and to a repeat
// of it, the same request among theirs, and the same one new row in their
// household's replica, before and after the receiver declines. Only
// accepting shows them anything.
func TestAFriendRequestSaysNothingOfWhoHoldsTheEmail(t *testing.T) {
	type setup struct {
		email string
		// after acts once the request is sent: as the receiver, if any.
		after func(e *env, id uid.ID)
	}
	other := func(e *env) user { return e.adult("other@example.test", false) }
	decline := func(token string) func(e *env, id uid.ID) {
		return func(e *env, id uid.ID) {
			in := e.friendRequests(token).Incoming
			if len(in) != 1 || in[0].Id != id {
				e.t.Fatalf("not received: %+v", in)
			}
			e.expect(204, "DELETE", "/friend-requests/"+itoa(id), token, nil, nil)
			if in := e.friendRequests(token).Incoming; len(in) != 0 {
				e.t.Fatalf("a declined request is still listed: %+v", in)
			}
		}
	}
	nothing := func(*env, uid.ID) {}
	cases := map[string]func(e *env) setup{
		"no such account": func(*env) setup { return setup{"nobody@example.test", nothing} },
		"an adult who has not answered": func(e *env) setup {
			o := other(e)
			return setup{o.account.Email, nothing}
		},
		"an adult who declines": func(e *env) setup {
			o := other(e)
			return setup{o.account.Email, decline(o.token)}
		},
		"an adult in a household with a kid": func(e *env) setup {
			o := other(e)
			e.kid(o, "Mia")
			return setup{o.account.Email, nothing}
		},
		"a pending adult": func(e *env) setup {
			e.expect(201, "POST", "/household/adults", e.admin.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, nil)
			return setup{"dee@example.test", nothing}
		},
		"a revoked adult": func(e *env) setup {
			o := other(e)
			e.expect(200, "PUT", "/accounts/"+itoa(o.account.ID), e.admin.token,
				map[string]any{"kind": "adult", "name": "other", "email": o.account.Email, "revoked": true}, nil)
			return setup{o.account.Email, nothing}
		},
		"the sender's own email": func(e *env) setup { return setup{"  PLAIN@example.test ", nothing} },
		"a friend already": func(e *env) setup {
			o := other(e)
			e.befriend(o, e.plain)
			return setup{o.account.Email, nothing}
		},
		"an adult who asked first": func(e *env) setup {
			o := other(e)
			e.expect(202, "POST", "/friend-requests", o.token, map[string]any{"email": e.plain.account.Email}, nil)
			return setup{o.account.Email, nothing}
		},
		"an adult who declined an earlier request": func(e *env) setup {
			o := other(e)
			var r api.OutgoingFriendRequest
			e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": o.account.Email}, &r)
			decline(o.token)(e, r.Id)
			e.expect(204, "DELETE", "/friend-requests/"+itoa(r.Id), e.plain.token, nil, nil)
			return setup{o.account.Email, nothing}
		},
		"an adult who ended the friendship": func(e *env) setup {
			o := other(e)
			e.befriend(e.plain, o)
			e.expect(204, "DELETE", "/friends/"+itoa(e.plain.account.ID), o.token, nil, nil)
			return setup{o.account.Email, nothing}
		},
	}
	type seen struct{ answer, again, listed, replica string }
	observe := func(t *testing.T, s func(e *env) setup) (seen, *env) {
		e := synced(t)
		c := s(e)
		friendsBefore := e.friends(e.plain.token)
		before := e.replicaRows(e.plain.token)
		status, raw := e.sendFriend(e.plain.token, c.email)
		var r api.OutgoingFriendRequest
		must(t, json.Unmarshal(raw, &r))
		c.after(e, r.Id)
		status2, raw2 := e.sendFriend(e.plain.token, c.email)
		var r2 api.OutgoingFriendRequest
		must(t, json.Unmarshal(raw2, &r2))
		again := sent(t, status2, raw2, c.email)
		if r2 != r {
			again += ", another request"
		}
		out := e.friendRequests(e.plain.token).Outgoing
		listed := "missing"
		if i := slices.IndexFunc(out, func(o api.OutgoingFriendRequest) bool { return o.Id == r.Id }); i >= 0 && out[i] == r {
			listed = "as sent"
		}
		if !slices.Equal(e.friends(e.plain.token), friendsBefore) {
			listed += ", friends changed"
		}
		after := e.replicaRows(e.plain.token)
		added, gone := minus(after, before), minus(before, after)
		replica := fmt.Sprintf("added %d, gone %d", len(added), len(gone))
		if len(added) == 1 && strings.HasPrefix(added[0], "friend_request:") && strings.Contains(added[0], r.Email) {
			replica += ", the request"
		}
		return seen{sent(t, status, raw, c.email), again, listed, replica}, e
	}
	want, _ := observe(t, cases["no such account"])
	if want.answer != `202 {"createdAt":"T","email":"EMAIL","expiresAt":"T","id":"ID"}` || want.listed != "as sent" ||
		want.replica != "added 1, gone 0, the request" {
		t.Fatalf("baseline %+v", want)
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if got, _ := observe(t, s); got != want {
				t.Errorf("the sender tells this apart:\n got %+v\nwant %+v", got, want)
			}
		})
	}
	// The one thing a sender may learn: an acceptance.
	got, _ := observe(t, func(e *env) setup {
		o := other(e)
		return setup{o.account.Email, func(e *env, id uid.ID) {
			e.expect(204, "POST", "/friend-requests/"+itoa(id)+"/accept", o.token, nil, nil)
		}}
	})
	if got == want {
		t.Fatal("an acceptance shows the sender nothing")
	}
}

// A request reaches the receiver's households alone, as an incoming row
// naming them and a card of its sender; a decline reaches them alone too.
func TestAFriendRequestReachesOnlyItsReceiver(t *testing.T) {
	e := synced(t)
	r := e.adult("r@example.test", false)
	third := e.adult("third@example.test", false)
	before := e.replicaRows(third.token)
	var out api.OutgoingFriendRequest
	e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"id": uid.N(4242), "email": " R@Example.test"}, &out)
	if out.Id != uid.N(4242) || out.Email != "R@Example.test" {
		t.Fatalf("the client's id and the email as written: %+v", out)
	}
	e.expect(409, "POST", "/friend-requests", third.token, map[string]any{"id": uid.N(4242), "email": "x@example.test"}, nil)
	e.expect(400, "POST", "/friend-requests", e.plain.token, map[string]any{"email": "not an email"}, nil)

	in := e.friendRequests(r.token).Incoming
	if len(in) != 1 || in[0].Id != out.Id || in[0].FromProfileId != e.plain.account.ID || in[0].FromName != e.plain.account.Name ||
		!in[0].ExpiresAt.Equal(out.ExpiresAt) {
		t.Fatalf("received %+v", in)
	}
	if l := e.friendRequests(third.token); len(l.Incoming)+len(l.Outgoing) != 0 {
		t.Fatalf("a third adult lists %+v", l)
	}
	if l := e.friendRequests(e.plain.token); len(l.Incoming) != 0 || len(l.Outgoing) != 1 {
		t.Fatalf("the sender lists %+v", l)
	}
	rows := e.replicaRows(r.token)
	hasPrefix := func(rows []string, p string) bool {
		return slices.ContainsFunc(rows, func(s string) bool { return strings.HasPrefix(s, p) })
	}
	if !hasPrefix(rows, "friend_request_in:") || hasPrefix(rows, "friend_request:") ||
		!slices.ContainsFunc(rows, func(s string) bool {
			return strings.HasPrefix(s, "account_card:") && strings.Contains(s, e.plain.account.Name)
		}) {
		t.Fatalf("the receiver's replica %v", rows)
	}
	if s := e.replicaRows(e.plain.token); hasPrefix(s, "friend_request_in:") {
		t.Fatalf("the sender's replica holds the receiver's side: %v", s)
	}
	e.expect(404, "POST", "/friend-requests/"+itoa(out.Id)+"/accept", e.plain.token, nil, nil)
	e.expect(404, "POST", "/friend-requests/"+itoa(out.Id)+"/accept", third.token, nil, nil)
	e.expect(404, "DELETE", "/friend-requests/"+itoa(out.Id), third.token, nil, nil)
	e.expect(204, "DELETE", "/friend-requests/"+itoa(out.Id), r.token, nil, nil)
	if !hasPrefix(e.replicaRows(r.token), "friend_request_decline:") || hasPrefix(e.replicaRows(e.plain.token), "friend_request_decline:") {
		t.Fatal("a decline reaches the decliner's household alone")
	}
	e.expect(404, "POST", "/friend-requests/"+itoa(out.Id)+"/accept", r.token, nil, nil)
	if after := e.replicaRows(third.token); !slices.Equal(after, before) {
		t.Fatalf("a third household's replica changed: %v", minus(after, before))
	}
}

// Friends offer each other as share and Watch together targets off TVs,
// never as profiles of the other's household, and bring nothing of their
// households: not their kids, not their profile pickers. Ending it is
// silent and leaves what they shared.
func TestFriendsShareAndWatchTogether(t *testing.T) {
	p := newPair(t)
	e := p.e
	r := e.adult("r@example.test", false)
	k := e.kid(r, "Mia")
	e.rate(p.movie, "G")
	e.befriend(e.plain, r)
	if f := e.friends(e.plain.token); !slices.Equal(f, []uid.ID{r.account.ID}) {
		t.Fatalf("friends %v", f)
	}
	if f := e.friends(r.token); !slices.Equal(f, []uid.ID{e.plain.account.ID}) {
		t.Fatalf("friends from the other side %v", f)
	}
	if l := e.friendRequests(r.token); len(l.Incoming)+len(l.Outgoing) != 0 {
		t.Fatalf("an accepted request stays: %+v", l)
	}
	candidates := func(u user, token string) []uid.ID {
		var page api.ShareCandidatePage
		e.expect(200, "GET", "/profiles/"+itoa(u.account.ID)+"/share-candidates", token, nil, &page)
		var out []uid.ID
		for _, c := range page.Items {
			out = append(out, c.ProfileId)
		}
		return out
	}
	if c := candidates(e.plain, e.plain.token); !slices.Contains(c, r.account.ID) || slices.Contains(c, k.Id) {
		t.Fatalf("share candidates %v", c)
	}
	if c := candidates(r, r.token); !slices.Contains(c, e.plain.account.ID) {
		t.Fatalf("share candidates from the other side %v", c)
	}
	tv, _ := e.secondDevice(e.plain, "TV", "roku")
	if c := candidates(e.plain, tv); slices.Contains(c, r.account.ID) {
		t.Fatalf("a friend is a share candidate on a TV: %v", c)
	}
	var profiles api.AccountPage
	e.expect(200, "GET", "/profiles", e.plain.token, nil, &profiles)
	if profileByID(profiles, r.account.ID) != nil {
		t.Fatal("a friend is in the household's profiles")
	}
	var pl api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Ours", "kind": "movies"}, &pl)
	shares := "/profiles/" + itoa(e.plain.account.ID) + "/playlists/" + itoa(pl.Id) + "/shares"
	e.expect(201, "POST", shares, e.plain.token, map[string]any{"profileId": r.account.ID, "access": "read"}, nil)
	e.expect(400, "POST", shares, e.plain.token, map[string]any{"profileId": k.Id, "access": "read"}, nil)

	w := p.open(p.helga, p.movie)
	if l := p.list(r.token); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("a friend lists %+v", l)
	}
	if l := p.list(e.kidDevice(r, k.Id)); len(l) != 0 {
		t.Fatalf("a friend's kid lists %+v", l)
	}
	e.expect(200, "POST", togetherPath(w)+"/join", r.token, nil, nil)

	e.expect(204, "DELETE", "/friends/"+itoa(e.plain.account.ID), r.token, nil, nil)
	e.expect(404, "DELETE", "/friends/"+itoa(e.plain.account.ID), r.token, nil, nil)
	if len(e.friends(e.plain.token))+len(e.friends(r.token)) != 0 {
		t.Fatal("a friendship ended stays")
	}
	if got := p.poll(r.token, w); got.Status != api.Joined {
		t.Fatalf("a member keeps it: %+v", got)
	}
	e.expect(200, "GET", "/profiles/"+itoa(r.account.ID)+"/playlists/"+itoa(pl.Id), r.token, nil, nil)
	if c := candidates(e.plain, e.plain.token); slices.Contains(c, r.account.ID) {
		t.Fatalf("an old friend is a share candidate: %v", c)
	}
	p.move(time.Minute)
	w = p.open(p.helga, p.movie)
	if l := p.list(r.token); slices.ContainsFunc(l, func(x api.WatchTogether) bool { return x.Id == w.Id }) {
		t.Fatalf("an old friend lists %+v", l)
	}
}

// A kid has no part in friends: every operation answers 403.
func TestFriendsAreForAdultsOnly(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Mia")
	kid := e.kidDevice(e.plain, k.Id)
	id := itoa(uid.N(7))
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/friends", nil}, {"DELETE", "/friends/" + id, nil}, {"GET", "/friend-requests", nil},
		{"POST", "/friend-requests", map[string]any{"email": "admin@example.test"}}, {"DELETE", "/friend-requests/" + id, nil},
		{"POST", "/friend-requests/" + id + "/accept", nil},
	} {
		e.expect(403, c.method, c.path, kid, c.body, nil)
	}
}

// One adult sends at most FriendRequestsPerDay within a day, repeats
// counted; the limit is theirs alone.
func TestFriendRequestsAreLimited(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	e.svc.Friends.Now = func() time.Time { return now }
	for i := range service.FriendRequestsPerDay {
		e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": fmt.Sprintf("n%d@example.test", i%3)}, nil)
	}
	raw := e.expect(429, "POST", "/friend-requests", e.plain.token, map[string]any{"email": "n0@example.test"}, nil)
	if errorCode(t, raw) != "too_many_requests" {
		t.Fatalf("%s", raw)
	}
	e.expect(202, "POST", "/friend-requests", e.admin.token, map[string]any{"email": "n0@example.test"}, nil)
	now = now.Add(service.FriendRequestWindow)
	e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": "n0@example.test"}, nil)
}

// A request no one accepts lists until it expires, for both, and then goes.
func TestFriendRequestsExpire(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	e.svc.Friends.Now = func() time.Time { return now }
	var r api.OutgoingFriendRequest
	e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": e.admin.account.Email}, &r)
	now = now.Add(service.FriendRequestLife - time.Millisecond)
	if len(e.friendRequests(e.plain.token).Outgoing) != 1 || len(e.friendRequests(e.admin.token).Incoming) != 1 {
		t.Fatal("gone before it expires")
	}
	now = now.Add(time.Millisecond)
	if len(e.friendRequests(e.plain.token).Outgoing)+len(e.friendRequests(e.admin.token).Incoming) != 0 {
		t.Fatal("listed once expired")
	}
	e.expect(404, "POST", "/friend-requests/"+itoa(r.Id)+"/accept", e.admin.token, nil, nil)
	if n, err := e.svc.Friends.Expire(); err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	var again api.OutgoingFriendRequest
	e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": e.admin.account.Email}, &again)
	if again.Id == r.Id {
		t.Fatal("an expired request answers a new one")
	}
}

// Deleting an account takes its friendships, its requests and its declines
// with it; requests to its email stay as their senders sent them. A kid
// made an adult receives what was sent to their new email.
func TestFriendRowsFollowTheirAccounts(t *testing.T) {
	e := newEnv(t)
	r := e.adult("r@example.test", false)
	third := e.adult("third@example.test", false)
	e.befriend(e.plain, r)
	var toR, fromR api.OutgoingFriendRequest
	e.expect(202, "POST", "/friend-requests", third.token, map[string]any{"email": r.account.Email}, &toR)
	e.expect(204, "DELETE", "/friend-requests/"+itoa(toR.Id), r.token, nil, nil)
	e.expect(202, "POST", "/friend-requests", r.token, map[string]any{"email": third.account.Email}, &fromR)
	e.expect(204, "DELETE", "/accounts/"+itoa(r.account.ID), e.admin.token, nil, nil)
	count := func(q string) int {
		var n int
		must(t, e.db.QueryRow(q, r.account.ID).Scan(&n))
		return n
	}
	if n := count(`SELECT (SELECT COUNT(*) FROM friendship WHERE ?1 IN (a_id, b_id)) + (SELECT COUNT(*) FROM friend_request WHERE from_id = ?1)
		+ (SELECT COUNT(*) FROM friend_request_decline WHERE account_id = ?1)`); n != 0 {
		t.Fatalf("%d rows of a deleted account", n)
	}
	if len(e.friends(e.plain.token)) != 0 || len(e.friendRequests(third.token).Incoming) != 0 {
		t.Fatal("a deleted account is still a friend or asking")
	}
	if out := e.friendRequests(third.token).Outgoing; len(out) != 1 || out[0] != toR {
		t.Fatalf("a request to a deleted account's email changed: %+v", out)
	}

	k := e.kid(e.plain, "Mia")
	e.expect(202, "POST", "/friend-requests", third.token, map[string]any{"email": "mia@example.test"}, nil)
	e.expect(201, "POST", "/profiles/"+itoa(k.Id)+"/make-adult", e.plain.token, map[string]any{"email": "mia@example.test"}, nil)
	mia, err := e.svc.Accounts.Repo().Get(k.Id)
	must(t, err)
	if got, err := e.svc.Friends.Received(mia); err != nil || len(got) != 1 || got[0].FromID != third.account.ID {
		t.Fatalf("a kid made an adult receives %+v %v", got, err)
	}
}
