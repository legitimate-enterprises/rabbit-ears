package uid

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewIsV7AndStrictlyIncreasing(t *testing.T) {
	prev := New()
	for range 10000 {
		id := New()
		if id.Compare(prev) <= 0 {
			t.Fatalf("%s not after %s", id, prev)
		}
		if _, err := Parse(id.String()); err != nil {
			t.Fatal(err)
		}
		prev = id
	}
}

func TestParse(t *testing.T) {
	const good = "01890a5d-ac96-774b-bcce-b302099a8057"
	id, err := Parse(good)
	if err != nil || id.String() != good {
		t.Fatalf("Parse(%q) = %s, %v", good, id, err)
	}
	for _, bad := range []string{
		"",
		strings.ToUpper(good),
		"01890a5d-ac96-474b-bcce-b302099a8057", // version 4
		"01890a5d-ac96-774b-ccce-b302099a8057", // variant 110x
		"01890a5dac96774bbcceb302099a8057",
		"01890a5d-ac96-774b-bcce-b302099a805",
		"01890a5d-ac96-774b-bcce-b302099a80570",
		"01890a5d_ac96-774b-bcce-b302099a8057",
		"0189Oa5d-ac96-774b-bcce-b302099a8057",
		"{1890a5d-ac96-774b-bcce-b302099a8057",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestN(t *testing.T) {
	if got := N(1).String(); got != "00000000-0000-7000-8000-000000000001" {
		t.Fatal(got)
	}
	if got := N(1<<48 - 1).String(); got != "00000000-0000-7000-8000-ffffffffffff" {
		t.Fatal(got)
	}
	if _, err := Parse(N(42).String()); err != nil {
		t.Fatal(err)
	}
	if N(1<<48-1).Compare(New()) >= 0 || N(1).Compare(N(2)) >= 0 {
		t.Fatal("N order")
	}
	for _, n := range []int{-1, 1 << 48} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("N(%d) did not panic", n)
				}
			}()
			N(n)
		}()
	}
}

func TestText(t *testing.T) {
	type row struct {
		ID  ID  `json:"id"`
		Ref *ID `json:"ref,omitempty"`
	}
	b, err := json.Marshal(row{ID: N(7)})
	if err != nil || string(b) != `{"id":"00000000-0000-7000-8000-000000000007"}` {
		t.Fatalf("%s %v", b, err)
	}
	var r row
	if err := json.Unmarshal([]byte(`{"id":"00000000-0000-7000-8000-000000000007","ref":"00000000-0000-7000-8000-000000000008"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != N(7) || r.Ref == nil || *r.Ref != N(8) {
		t.Fatalf("%+v", r)
	}
	if json.Unmarshal([]byte(`{"id":"7"}`), &r) == nil {
		t.Fatal("accepted a malformed id")
	}
	if _, err := json.Marshal(row{}); err == nil {
		t.Fatal("marshaled the zero id")
	}
}

func TestSQL(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE t (id BLOB NOT NULL PRIMARY KEY, ref BLOB)`); err != nil {
		t.Fatal(err)
	}
	var none *ID
	ref := N(2)
	if _, err := db.Exec(`INSERT INTO t VALUES (?, ?), (?, ?)`, N(1), none, N(3), &ref); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (?, NULL)`, ID{}); err == nil {
		t.Fatal("bound the zero id")
	}
	var typ string
	var n int
	if err := db.QueryRow(`SELECT typeof(id), length(id) FROM t WHERE id = ?`, N(1)).Scan(&typ, &n); err != nil || typ != "blob" || n != 16 {
		t.Fatalf("%s %d %v", typ, n, err)
	}
	rows, err := db.Query(`SELECT id, ref FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var id ID
		var r *ID
		if err := rows.Scan(&id, &r); err != nil {
			t.Fatal(err)
		}
		s := id.String() + " "
		if r != nil {
			s += r.String()
		}
		got = append(got, s)
	}
	want := []string{"00000000-0000-7000-8000-000000000001 ", "00000000-0000-7000-8000-000000000003 00000000-0000-7000-8000-000000000002"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
	var id ID
	if err := db.QueryRow(`SELECT ref FROM t WHERE id = ?`, N(1)).Scan(&id); err == nil {
		t.Fatal("scanned NULL into an ID")
	}
	if err := db.QueryRow(`SELECT 'x'`).Scan(&id); err == nil {
		t.Fatal("scanned TEXT into an ID")
	}
}
