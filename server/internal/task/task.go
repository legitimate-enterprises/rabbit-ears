// Package task holds scheduled tasks and the record of each run, with their
// repository, for the admin Tasks page.
package task

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Run statuses.
const (
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
)

// Task is a named unit of recurring work. A nil interval means run on demand only.
type Task struct {
	ID              uid.ID
	Name            string
	Kind            string
	IntervalSeconds *int
	Enabled         bool
	NextRunAt       *time.Time
	RunRequestedAt  *time.Time // Run now, pending; cleared when the run starts
	CreatedAt       time.Time
	ModifiedAt      time.Time
}

// Run is one execution of a task.
type Run struct {
	ID         uid.ID
	TaskID     uid.ID
	Status     string
	Progress   float64 // 0 to 1
	Error      string
	Summary    string // one human-readable line on what the run did; empty if it reports none
	StartedAt  time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// Repo persists tasks and runs.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const tcols = `id, name, kind, interval_seconds, enabled, next_run_at, created_at, run_requested_at, modified_at`

func scanTask(s db.Scanner) (*Task, error) {
	var t Task
	var iv, next, req sql.NullInt64
	var created, modified int64
	if err := s.Scan(&t.ID, &t.Name, &t.Kind, &iv, &t.Enabled, &next, &created, &req, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	if iv.Valid {
		v := int(iv.Int64)
		t.IntervalSeconds = &v
	}
	t.NextRunAt, t.RunRequestedAt, t.CreatedAt, t.ModifiedAt = db.FromNullMs(next), db.FromNullMs(req), db.FromMs(created), db.FromMs(modified)
	return &t, nil
}

// Create inserts t, minting its ID unless set; names are unique.
func (r *Repo) Create(t *Task) error {
	if t.ID.IsZero() {
		t.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.CreatedAt, t.ModifiedAt = now, now
	var iv any
	if t.IntervalSeconds != nil {
		iv = *t.IntervalSeconds
	}
	_, err := r.d.Exec(`INSERT INTO task (id, name, kind, interval_seconds, enabled, next_run_at, created_at, modified_at, run_requested_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, t.ID, t.Name, t.Kind, iv, t.Enabled, db.NullMs(t.NextRunAt), db.Ms(now), db.Ms(now), db.NullMs(t.RunRequestedAt))
	return db.Wrap(err)
}

// Get returns one task or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Task, error) {
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
	t.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	return db.ExecOne(r.d, `UPDATE task SET name=?, kind=?, interval_seconds=?, enabled=?, next_run_at=?, run_requested_at=?, modified_at=? WHERE id=?`,
		t.Name, t.Kind, iv, t.Enabled, db.NullMs(t.NextRunAt), db.NullMs(t.RunRequestedAt), db.Ms(t.ModifiedAt), t.ID)
}

// Reschedule writes only a task's next run and pending Run now.
func (r *Repo) Reschedule(id uid.ID, next, requested *time.Time) error {
	return db.ExecOne(r.d, `UPDATE task SET next_run_at = ?, run_requested_at = ?, modified_at = ? WHERE id = ?`,
		db.NullMs(next), db.NullMs(requested), db.Ms(time.Now()), id)
}

// RequestRun asks for a run of the task now, whether or not it is enabled
// or tasks are paused.
func (r *Repo) RequestRun(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE task SET run_requested_at = ?1, modified_at = ?1 WHERE id = ?2`, db.Ms(at), id)
}

// Requested lists tasks with a pending Run now, oldest request first.
func (r *Repo) Requested() ([]*Task, error) {
	return r.tasks(`SELECT ` + tcols + ` FROM task WHERE run_requested_at IS NOT NULL ORDER BY run_requested_at, id`)
}

// Delete removes a task and its runs.
func (r *Repo) Delete(id uid.ID) error { return db.ExecOne(r.d, `DELETE FROM task WHERE id = ?`, id) }

const rcols = `id, task_id, status, progress, error, started_at, finished_at, summary, created_at, modified_at`

func scanRun(s db.Scanner) (*Run, error) {
	var v Run
	var errText, summary sql.NullString
	var started, created, modified int64
	var finished sql.NullInt64
	if err := s.Scan(&v.ID, &v.TaskID, &v.Status, &v.Progress, &errText, &started, &finished, &summary, &created, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	v.Error, v.Summary, v.StartedAt, v.FinishedAt = errText.String, summary.String, db.FromMs(started), db.FromNullMs(finished)
	v.CreatedAt, v.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &v, nil
}

// StartRun records a new running run for a task.
func (r *Repo) StartRun(taskID uid.ID, at time.Time) (*Run, error) {
	id, now := uid.New(), db.Ms(time.Now())
	if _, err := r.d.Exec(`INSERT INTO task_run (id, task_id, status, started_at, created_at, modified_at) VALUES (?, ?, 'running', ?, ?, ?)`,
		id, taskID, db.Ms(at), now, now); err != nil {
		return nil, db.Wrap(err)
	}
	return r.Run(id)
}

// Run returns one run or db.ErrNotFound.
func (r *Repo) Run(id uid.ID) (*Run, error) {
	return scanRun(r.d.QueryRow(`SELECT `+rcols+` FROM task_run WHERE id = ?`, id))
}

// SetProgress updates a running run's progress.
func (r *Repo) SetProgress(id uid.ID, progress float64) error {
	return db.ExecOne(r.d, `UPDATE task_run SET progress = ?, modified_at = ? WHERE id = ? AND status = 'running'`, progress, db.Ms(time.Now()), id)
}

// SetSummary records what a running run did.
func (r *Repo) SetSummary(id uid.ID, summary string) error {
	return db.ExecOne(r.d, `UPDATE task_run SET summary = ?, modified_at = ? WHERE id = ? AND status = 'running'`, db.NullStr(summary), db.Ms(time.Now()), id)
}

// FinishRun closes a running run as succeeded (empty errText) or failed.
func (r *Repo) FinishRun(id uid.ID, errText string, at time.Time) error {
	status, progress := Succeeded, "progress = 1, "
	if errText != "" {
		status, progress = Failed, ""
	}
	return db.ExecOne(r.d, `UPDATE task_run SET `+progress+`status = ?1, error = ?2, finished_at = ?3, modified_at = ?3 WHERE id = ?4 AND status = 'running'`,
		status, db.NullStr(errText), db.Ms(at), id)
}

func (r *Repo) runs(q string, args ...any) ([]*Run, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scanRun)
}

// History lists finished runs, newest first, optionally of one task (nil for all).
func (r *Repo) History(taskID *uid.ID, limit int) ([]*Run, error) {
	return r.runs(`SELECT `+rcols+` FROM task_run WHERE status != 'running' AND (?1 IS NULL OR task_id = ?1)
		ORDER BY started_at DESC, id DESC LIMIT ?2`, taskID, limit)
}

// LastRun returns a task's most recent run, or db.ErrNotFound.
func (r *Repo) LastRun(taskID uid.ID) (*Run, error) {
	return scanRun(r.d.QueryRow(`SELECT `+rcols+` FROM task_run WHERE task_id = ? ORDER BY started_at DESC, id DESC LIMIT 1`, taskID))
}

// FailInterrupted marks runs left running by a crash as failed and returns
// the tasks they belonged to.
func (r *Repo) FailInterrupted(at time.Time) ([]uid.ID, error) {
	rows, err := r.d.Query(`UPDATE task_run SET status = 'failed', error = 'interrupted', finished_at = ?1, modified_at = ?1 WHERE status = 'running'
		RETURNING task_id`, db.Ms(at))
	return db.Collect(rows, err, db.Column[uid.ID])
}
