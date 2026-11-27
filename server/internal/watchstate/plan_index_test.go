package watchstate

import (
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Show progress backs the TV grid on every device, so for every kind of
// audience it reads items and watch state by index, never a whole table;
// only its own intermediate results are scanned.
func TestShowProgressUsesIndexes(t *testing.T) {
	d := dbtest.Open(t)
	kid := uid.N(1)
	for _, a := range []item.Audience{{}, {Kid: true, AccountID: &kid, TV: []string{"TV-Y"}}, {Kid: true, AccountID: &kid, AllowList: true}} {
		rows, err := d.Query(`EXPLAIN QUERY PLAN `+showProgress, append([]any{kid, nil, nil, 50, 0}, a.Args()...)...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		if n := strings.Count(strings.Join(plan, "\n"), "INDEX item_kind_title"); n != 1 {
			t.Fatalf("only the page of shows walks the kind index; seasons and episodes come by parent:\n%s", strings.Join(plan, "\n"))
		}
		for _, step := range plan {
			for _, table := range []string{"item", "watch_state", "item_metadata", "account_allow"} {
				if strings.HasPrefix(step, "SCAN "+table) && (len(step) == len("SCAN "+table) || step[len("SCAN "+table)] == ' ') {
					t.Fatalf("%+v scans %s:\n%s", a, table, strings.Join(plan, "\n"))
				}
			}
		}
	}
}
