package replica_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// A touched key names its id parts by canonical UUID string, as diffs do,
// and its other parts by their declared types.
func TestTouchable(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v4 := "0b6c1d6e-4f0a-4c3e-9d2b-7e5f8a9c1d2e"
	for _, c := range []struct {
		table string
		key   []any
		ok    bool
	}{
		{"watch_state", []any{u(103), u(1)}, true},
		{"occasion_keyword", []any{u(301), float64(900)}, true},
		{"occasion_phrase", []any{u(301), "Snow"}, true},
		{"server_info", []any{float64(1)}, true},
		{"watch_state", []any{float64(103), float64(1)}, false},
		{"watch_state", []any{strings.ToUpper(u(0xab)), u(1)}, false},
		{"watch_state", []any{v4, u(1)}, false},
		{"watch_state", []any{u(103)}, false},
		{"occasion_keyword", []any{u(301), "900"}, false},
		{"occasion_keyword", []any{u(301), 900.5}, false},
		{"occasion_phrase", []any{u(301), float64(1)}, false},
		{"no_such_table", []any{u(1)}, false},
	} {
		ok, err := s.Touchable(context.Background(), replica.V1, c.table, c.key)
		if err != nil || ok != c.ok {
			t.Errorf("%s %v: %v, %v", c.table, c.key, ok, err)
		}
	}
}

// Answering restamps a touched row in scope and tombstones a touched row
// that is gone, its id parts the 16 bytes the triggers write, so the next
// diff carries both as the client named them.
func TestAnswerTouches(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	err := s.Answer(uid.N(201), uid.N(1), "00000000-0000-4000-8000-000000000001", "rejected", nil, []replica.Row{
		{Table: "watch_state", Key: []any{u(103), u(2)}},
		{Table: "watch_state", Key: []any{u(103), u(9)}},
		{Table: "watch_state", Key: []any{u(112), u(1)}},
		{Table: "occasion_keyword", Key: []any{u(301), float64(999)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := tombstone(t, d, "watch_state", uid.N(103), uid.N(9)); got <= v {
		t.Errorf("gone row's tombstone at %d, clock was %d", got, v)
	}
	if got := tombstone(t, d, "occasion_keyword", uid.N(301), 999); got <= v {
		t.Errorf("gone keyword's tombstone at %d, clock was %d", got, v)
	}
	got, _ := pass(t, s, uid.N(201), v, 100, nil)
	slices.Sort(got)
	want := []string{
		"occasion_keyword:[" + u(301) + " 999]=deleted",
		"watch_state:[" + u(103) + " " + u(2) + "]",
		"watch_state:[" + u(103) + " " + u(9) + "]=deleted",
	}
	if len(got) != len(want) {
		t.Fatalf("diff %v", got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("diff %v, want %v", got, want)
		}
	}
	if result, _, ok, err := s.Answered(context.Background(), uid.N(1), "00000000-0000-4000-8000-000000000001"); err != nil || !ok || result != "rejected" {
		t.Errorf("answered %q, %v, %v", result, ok, err)
	}
	if err := s.Answer(uid.N(201), uid.N(1), "00000000-0000-4000-8000-000000000002", "accepted", nil,
		[]replica.Row{{Table: "watch_state", Key: []any{float64(103), float64(2)}}}); err == nil {
		t.Error("an untouchable key was answered")
	}
}
