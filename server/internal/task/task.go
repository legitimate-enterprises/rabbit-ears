// Package task holds scheduled tasks and the record of each run, with their
// repository, for the admin Tasks page.
package task

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Run statuses.
const (
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
)

// Task is a named unit of recurring work. A nil interval means run on demand only.
type Task struct {
	ID              int64
	Name            string
	Kind            string
	IntervalSeconds *int
	Enabled         bool
	NextRunAt       *time.Time
	RunRequestedAt  *time.Time // Run now, pending; cleared when the run starts
	CreatedAt       time.Time
}

// Run is one execution of a task.
type Run struct {
	ID         int64
	TaskID     int64
	Status     string
	Progress   float64 // 0 to 1
	Error      string
	Summary    string // one human-readable line on what the run did; empty if it reports none
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Repo persists tasks and runs.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const tcols = `id, name, kind, interval_seconds, enabled, next_run_at, created_at, run_requested_at`

func scanTask(s db.Scanner) (*Task, error) {
	var t Task
	var iv, next, req sql.NullInt64
	var created int64
	if err := s.Scan(&t.ID, &t.Name, &t.Kind, &iv, &t.Enabled, &next, &created, &req); err != nil {
		return nil, db.Wrap(err)
	}
	if iv.Valid {
		v := int(iv.Int64)
		t.IntervalSeconds = &v
	}
	t.NextRunAt, t.RunRequestedAt, t.CreatedAt = db.FromNullMs(next), db.FromNullMs(req), db.FromMs(created)
	return &t, nil
}

// Create inserts t; names are unique.
func (r *Repo) Create(t *Task) error {
	t.CreatedAt = time.Now().UTC()
	var iv any
	if t.IntervalSeconds != nil {
		iv = *t.IntervalSeconds
	}
	res, err := r.d.Exec(`INSERT INTO task (name, kind, interval_seconds, enabled, next_run_at, created_at, run_requested_at) VALUES (?,?,?,?,?,?,?)`,
		t.Name, t.Kind, iv, t.Enabled, db.NullMs(t.NextRunAt), db.Ms(t.CreatedAt), db.NullMs(t.RunRequestedAt))
	if err != nil {
		return db.Wrap(err)
	}
	t.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one task or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Task, error) {
	return scanTask(r.d.QueryRow(`SELECT `+tcols+` FROM task WHERE id = ?`, id))
}

// ByName finds a task by its unique name.
func (r *Repo) ByName(name string) (*Task, error) {
	return scanTask(r.d.QueryRow(`SELECT `+tcols+` FROM task WHERE name = ?`, name))
}

// List returns every task by name.
func (r *Repo) List() ([]*Task, error) {
	return r.tasks(`SELECT ` + tcols + ` FROM task ORDER BY name`)
}

func (r *Repo) tasks(q string, args ...any) ([]*Task, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scanTask)
}

// Due lists enabled tasks whose next run is at or before now, oldest first.
func (r *Repo) Due(now time.Time) ([]*Task, error) {
	return r.tasks(`SELECT `+tcols+` FROM task WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at, id`, db.Ms(now))
}

// Update writes every mutable field of t.
func (r *Repo) Update(t *Task) error {
	var iv any
	if t.IntervalSeconds != nil {
		iv = *t.IntervalSeconds
	}
	return db.ExecOne(r.d, `UPDATE task SET name=?, kind=?, interval_seconds=?, enabled=?, next_run_at=?, run_requested_at=? WHERE id=?`,
		t.Name, t.Kind, iv, t.Enabled, db.NullMs(t.NextRunAt), db.NullMs(t.RunRequestedAt), t.ID)
}

// Reschedule writes only a task's next run and pending Run now.
func (r *Repo) Reschedule(id int64, next, requested *time.Time) error {
	return db.ExecOne(r.d, `UPDATE task SET next_run_at = ?, run_requested_at = ? WHERE id = ?`, db.NullMs(next), db.NullMs(requested), id)
}

// RequestRun asks for a run of the task now, whether or not it is enabled
// or tasks are paused.
func (r *Repo) RequestRun(id int64, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE task SET run_requested_at = ? WHERE id = ?`, db.Ms(at), id)
}

// Requested lists tasks with a pending Run now, oldest request first.
func (r *Repo) Requested() ([]*Task, error) {
	return r.tasks(`SELECT ` + tcols + ` FROM task WHERE run_requested_at IS NOT NULL ORDER BY run_requested_at, id`)
}

// Delete removes a task and its runs.
func (r *Repo) Delete(id int64) error { return db.ExecOne(r.d, `DELETE FROM task WHERE id = ?`, id) }

const rcols = `id, task_id, status, progress, error, started_at, finished_at, summary`

func scanRun(s db.Scanner) (*Run, error) {
	var v Run
	var errText, summary sql.NullString
	var started int64
	var finished sql.NullInt64
	if err := s.Scan(&v.ID, &v.TaskID, &v.Status, &v.Progress, &errText, &started, &finished, &summary); err != nil {
		return nil, db.Wrap(err)
	}
	v.Error, v.Summary, v.StartedAt, v.FinishedAt = errText.String, summary.String, db.FromMs(started), db.FromNullMs(finished)
	return &v, nil
}

// StartRun records a new running run for a task.
func (r *Repo) StartRun(taskID int64, at time.Time) (*Run, error) {
	res, err := r.d.Exec(`INSERT INTO task_run (task_id, status, started_at) VALUES (?, 'running', ?)`, taskID, db.Ms(at))
	if err != nil {
		return nil, db.Wrap(err)
	}
	id, _ := res.LastInsertId()
	return r.Run(id)
}

// Run returns one run or db.ErrNotFound.
func (r *Repo) Run(id int64) (*Run, error) {
	return scanRun(r.d.QueryRow(`SELECT `+rcols+` FROM task_run WHERE id = ?`, id))
}

// SetProgress updates a running run's progress.
func (r *Repo) SetProgress(id int64, progress float64) error {
	return db.ExecOne(r.d, `UPDATE task_run SET progress = ? WHERE id = ? AND status = 'running'`, progress, id)
}

// SetSummary records what a running run did.
func (r *Repo) SetSummary(id int64, summary string) error {
	return db.ExecOne(r.d, `UPDATE task_run SET summary = ? WHERE id = ? AND status = 'running'`, db.NullStr(summary), id)
}

// FinishRun closes a running run as succeeded (empty errText) or failed.
func (r *Repo) FinishRun(id int64, errText string, at time.Time) error {
	status, progress := Succeeded, "progress = 1, "
	if errText != "" {
		status, progress = Failed, ""
	}
	return db.ExecOne(r.d, `UPDATE task_run SET `+progress+`status = ?, error = ?, finished_at = ? WHERE id = ? AND status = 'running'`,
		status, db.NullStr(errText), db.Ms(at), id)
}

func (r *Repo) runs(q string, args ...any) ([]*Run, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scanRun)
}

// History lists finished runs, newest first, optionally of one task (0 for all).
func (r *Repo) History(taskID int64, limit int) ([]*Run, error) {
	return r.runs(`SELECT `+rcols+` FROM task_run WHERE status != 'running' AND (?1 = 0 OR task_id = ?1)
		ORDER BY started_at DESC, id DESC LIMIT ?2`, taskID, limit)
}

// LastRun returns a task's most recent run, or db.ErrNotFound.
func (r *Repo) LastRun(taskID int64) (*Run, error) {
	return scanRun(r.d.QueryRow(`SELECT `+rcols+` FROM task_run WHERE task_id = ? ORDER BY started_at DESC, id DESC LIMIT 1`, taskID))
}

// FailInterrupted marks runs left running by a crash as failed and returns
// the tasks they belonged to.
func (r *Repo) FailInterrupted(at time.Time) ([]int64, error) {
	rows, err := r.d.Query(`UPDATE task_run SET status = 'failed', error = 'interrupted', finished_at = ? WHERE status = 'running' RETURNING task_id`, db.Ms(at))
	return db.Collect(rows, err, func(s db.Scanner) (int64, error) {
		var id int64
		if err := s.Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	})
}
