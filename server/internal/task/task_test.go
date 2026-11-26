package task_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
)

func TestTaskCRUDAndDue(t *testing.T) {
	r := task.New(dbtest.Open(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	day := 86400
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	a := &task.Task{Name: "scan", Kind: "scan", IntervalSeconds: &day, Enabled: true, NextRunAt: &past}
	b := &task.Task{Name: "later", Kind: "scan", Enabled: true, NextRunAt: &future}
	c := &task.Task{Name: "off", Kind: "scan", Enabled: false, NextRunAt: &past}
	d := &task.Task{Name: "manual", Kind: "backup", Enabled: true}
	for _, x := range []*task.Task{a, b, c, d} {
		if err := r.Create(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Create(&task.Task{Name: "scan", Kind: "x"}); !errors.Is(err, db.ErrConflict) {
		t.Fatal(err)
	}
	zero := 0
	if err := r.Create(&task.Task{Name: "bad", Kind: "x", IntervalSeconds: &zero}); err == nil {
		t.Fatal("zero interval accepted")
	}
	due, _ := r.Due(now)
	if len(due) != 1 || due[0].Name != "scan" {
		t.Fatalf("%+v", due)
	}
	g, _ := r.ByName("scan")
	if *g.IntervalSeconds != day || !g.NextRunAt.Equal(past) || !g.Enabled {
		t.Fatalf("%+v", g)
	}
	if m, _ := r.Get(d.ID); m.IntervalSeconds != nil || m.NextRunAt != nil {
		t.Fatalf("%+v", m)
	}
	g.NextRunAt = &future
	g.Enabled = false
	if err := r.Update(g); err != nil {
		t.Fatal(err)
	}
	if due, _ := r.Due(now); len(due) != 0 {
		t.Fatal("nothing should be due")
	}
	if l, _ := r.List(); len(l) != 4 {
		t.Fatal(len(l))
	}
	if err := r.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(d); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRuns(t *testing.T) {
	r := task.New(dbtest.Open(t))
	tk := &task.Task{Name: "scan", Kind: "scan", Enabled: true}
	_ = r.Create(tk)
	t0 := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := r.StartRun(999, t0); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	if _, err := r.LastRun(tk.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	ok, _ := r.StartRun(tk.ID, t0)
	bad, _ := r.StartRun(tk.ID, t0.Add(time.Minute))
	if err := r.SetProgress(ok.ID, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProgress(ok.ID, 1.5); err == nil {
		t.Fatal("progress above 1 accepted")
	}
	if err := r.FinishRun(ok.ID, "", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.FinishRun(bad.ID, "tmdb down", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r.FinishRun(ok.ID, "", t0); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("finishing twice: %v", err)
	}
	if err := r.SetProgress(ok.ID, 0.1); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("progress on finished run: %v", err)
	}
	g, _ := r.Run(ok.ID)
	if g.Status != task.Succeeded || g.Progress != 1 || g.FinishedAt == nil || g.Error != "" {
		t.Fatalf("%+v", g)
	}
	last, _ := r.LastRun(tk.ID)
	if last.ID != bad.ID || last.Status != task.Failed || last.Error != "tmdb down" {
		t.Fatalf("%+v", last)
	}
	if h, _ := r.History(tk.ID, 10); len(h) != 2 || h[0].ID != bad.ID {
		t.Fatalf("%+v", h)
	}
	hung, _ := r.StartRun(tk.ID, t0.Add(time.Hour))
	if ids, _ := r.FailInterrupted(t0.Add(2 * time.Hour)); len(ids) != 1 || ids[0] != tk.ID {
		t.Fatal(ids)
	}
	if g, _ := r.Run(hung.ID); g.Status != task.Failed || g.Error != "interrupted" {
		t.Fatalf("%+v", g)
	}
	_ = r.Delete(tk.ID)
	if h, _ := r.History(0, 10); len(h) != 0 {
		t.Fatal("runs should cascade")
	}
}

func TestRunSummary(t *testing.T) {
	r := task.New(dbtest.Open(t))
	tk := &task.Task{Name: "Scan", Kind: "scan:1", Enabled: true}
	if err := r.Create(tk); err != nil {
		t.Fatal(err)
	}
	run, _ := r.StartRun(tk.ID, time.Now())
	if run.Summary != "" {
		t.Fatalf("summary %q", run.Summary)
	}
	if err := r.SetSummary(run.ID, "3 movies added"); err != nil {
		t.Fatal(err)
	}
	if err := r.FinishRun(run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.LastRun(tk.ID); got.Summary != "3 movies added" {
		t.Fatalf("summary %q", got.Summary)
	}
	if err := r.SetSummary(run.ID, "late"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("finished runs are closed: %v", err)
	}
}

func TestRunRequests(t *testing.T) {
	r := task.New(dbtest.Open(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	a := &task.Task{Name: "a", Kind: "scan", Enabled: true}
	b := &task.Task{Name: "b", Kind: "scan", Enabled: false}
	for _, x := range []*task.Task{a, b} {
		if err := r.Create(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RequestRun(a.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestRun(b.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestRun(999, now); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	req, _ := r.Requested()
	if len(req) != 2 || req[0].RunRequestedAt == nil || !req[0].RunRequestedAt.Equal(now) {
		t.Fatalf("a request runs even a disabled task: %+v", req)
	}
	a.RunRequestedAt = nil
	if err := r.Update(a); err != nil {
		t.Fatal(err)
	}
	if req, _ := r.Requested(); len(req) != 1 || req[0].ID != b.ID {
		t.Fatalf("%+v", req)
	}
}
