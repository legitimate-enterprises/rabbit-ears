package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// problemFiles makes one file of each problem in a Movies library, a
// healthy file, and a missing file whose last probe also failed.
func (e *env) problemFiles() (lib uid.ID, missing, noDuration, unreadable *mediafile.File) {
	e.t.Helper()
	lib = e.library("movies")
	now := time.Now().UTC().Truncate(time.Millisecond)
	files := e.svc.MediaFiles.Repo()
	file := func(title string) *mediafile.File {
		it := e.movie(lib, title)
		f := &mediafile.File{ItemID: &it, Path: "/movies/" + title + ".mkv", SizeBytes: 1, FileModifiedAt: now}
		must(e.t, files.CreateFile(f))
		return f
	}
	healthy := file("Healthy")
	healthy.ProbedAt, healthy.DurationMs = &now, new(int64(60000))
	must(e.t, files.UpdateFile(healthy))
	missing = file("Gone")
	must(e.t, files.SetMissing(missing, &now))
	goneUnreadable := file("Gone Unreadable")
	goneUnreadable.ProbeFailedAt, goneUnreadable.ProbeError = &now, "no"
	must(e.t, files.UpdateFile(goneUnreadable))
	must(e.t, files.SetMissing(goneUnreadable, &now))
	noDuration = file("Election Night")
	noDuration.ProbedAt, noDuration.DurationMs = &now, new(int64(0))
	must(e.t, files.UpdateFile(noDuration))
	unreadable = file("Pilot")
	unreadable.ProbeFailedAt, unreadable.ProbeError = &now, "Invalid data found when processing input"
	must(e.t, files.UpdateFile(unreadable))
	return lib, missing, noDuration, unreadable
}

func (e *env) problems(query string) []api.MediaFile {
	e.t.Helper()
	var page api.MediaFilePage
	e.expect(200, "GET", "/media-files?"+query, e.admin.token, nil, &page)
	return page.Items
}

// An admin lists the files with each problem, with their library and title,
// and only an admin may.
func TestListProblemFiles(t *testing.T) {
	e := newEnv(t)
	lib, missing, noDuration, unreadable := e.problemFiles()
	other := e.library("tv")

	got := e.problems("problem=unreadable")
	if len(got) != 1 || got[0].Id != unreadable.ID || *got[0].LibraryId != lib || *got[0].Title != "Pilot" || got[0].Path != "/movies/Pilot.mkv" ||
		got[0].ProbeFailedAt == nil || *got[0].ProbeError != "Invalid data found when processing input" || got[0].ProbedAt != nil {
		t.Fatalf("unreadable %+v", got)
	}
	if got = e.problems("problem=no_duration&libraryId=" + itoa(lib)); len(got) != 1 || got[0].Id != noDuration.ID {
		t.Fatalf("no duration %+v", got)
	}
	if got = e.problems("problem=missing"); len(got) != 2 || got[1].Id != missing.ID || got[1].MissingAt == nil {
		t.Fatalf("missing %+v", got)
	}
	if got = e.problems("problem=missing&libraryId=" + itoa(other)); len(got) != 0 {
		t.Fatalf("another library's %+v", got)
	}
	if got = e.problems("problem=missing&limit=1"); len(got) != 1 {
		t.Fatalf("paged %+v", got)
	}
	e.expect(400, "GET", "/media-files", e.admin.token, nil, nil)
	e.expect(400, "GET", "/media-files?problem=whim", e.admin.token, nil, nil)
	e.expect(403, "GET", "/media-files?problem=missing", e.plain.token, nil, nil)
}

// The counts are what the Needs attention page lists: open reviews, files
// that cannot be streamed, missing files, and tasks and conversions that failed.
func TestAttentionCountsWhatThePageLists(t *testing.T) {
	e := newEnv(t)
	var c api.Attention
	e.expect(200, "GET", "/attention", e.admin.token, nil, &c)
	if c != (api.Attention{}) {
		t.Fatalf("nothing yet %+v", c)
	}
	lib, missing, _, _ := e.problemFiles()
	now := time.Now().UTC()
	reviews := e.svc.Reviews.Repo()
	must(t, reviews.Raise(&review.Review{LibraryID: lib, Path: "/movies/a.mkv", Reason: review.Unrecognized, RaisedAt: now}))
	dismissed := &review.Review{LibraryID: lib, Path: "/movies/b.mkv", Reason: review.Unrecognized, RaisedAt: now}
	must(t, reviews.Raise(dismissed))
	must(t, reviews.SetDismissed(dismissed.ID, &now))
	tasks := e.svc.Tasks.Repo()
	for i, errText := range []string{"library folder /movies unreadable", "", "earlier failure"} {
		tk := &task.Task{Name: "T" + itoa(int64(i)), Kind: "k" + itoa(int64(i)), Enabled: true}
		must(t, tasks.Create(tk))
		run, err := tasks.StartRun(tk.ID, now)
		must(t, err)
		must(t, tasks.FinishRun(run.ID, errText, now))
		if i == 2 {
			run, err = tasks.StartRun(tk.ID, now.Add(time.Second))
			must(t, err)
			must(t, tasks.FinishRun(run.ID, "", now.Add(time.Second)))
		}
	}
	convs := e.svc.Downloads.Repo()
	for _, height := range []int{480, 720} {
		cv := &download.Conversion{MediaFileID: missing.ID, SourceModifiedAt: now, Container: "mp4",
			Entry: &session.PlanEntry{Verdict: "video_transcode", VideoAction: "convert", AudioAction: "copy", TopHeight: height}}
		must(t, convs.Request(cv))
		if height == 480 {
			must(t, convs.Fail(cv.ID, "ffmpeg exited with status 1", now))
		}
	}

	e.expect(200, "GET", "/attention", e.admin.token, nil, &c)
	var open api.IdentityReviewPage
	e.expect(200, "GET", "/identity-reviews?dismissed=false", e.admin.token, nil, &open)
	var failedConvs api.ConversionPage
	e.expect(200, "GET", "/conversions?failed=true", e.admin.token, nil, &failedConvs)
	var ts api.TaskPage
	e.expect(200, "GET", "/tasks?limit=200", e.admin.token, nil, &ts)
	failedTasks := 0
	for _, tk := range ts.Items {
		if tk.LastRun != nil && tk.LastRun.Status == api.TaskRunStatusFailed {
			failedTasks++
		}
	}
	want := api.Attention{Identity: len(open.Items), Unstreamable: len(e.problems("problem=no_duration")) + len(e.problems("problem=unreadable")),
		Missing: len(e.problems("problem=missing")), Failed: failedTasks + len(failedConvs.Items)}
	if c != want || c != (api.Attention{Identity: 1, Unstreamable: 2, Missing: 2, Failed: 2}) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if len(failedConvs.Items) != 1 || failedConvs.Items[0].Status != api.ConversionStatusFailed {
		t.Fatalf("failed conversions %+v", failedConvs)
	}
	e.expect(403, "GET", "/attention", e.plain.token, nil, nil)
}

// Read again forgets a file's probe so its library's scan, queued now,
// reads it again.
func TestReadAFileAgain(t *testing.T) {
	e := newEnv(t)
	lib, _, noDuration, _ := e.problemFiles()
	tk := &task.Task{Name: "Scan", Kind: scan.ScanKind(lib), Enabled: true}
	must(t, e.svc.Tasks.Repo().Create(tk))

	e.expect(202, "POST", "/media-files/"+itoa(noDuration.ID)+"/probe", e.admin.token, nil, nil)
	f, err := e.svc.MediaFiles.Repo().File(noDuration.ID)
	must(t, err)
	if f.ProbedAt != nil {
		t.Fatalf("probe kept %+v", f)
	}
	if tk, err = e.svc.Tasks.Repo().Get(tk.ID); err != nil || tk.RunRequestedAt == nil {
		t.Fatalf("scan not queued %+v %v", tk, err)
	}
	e.expect(404, "POST", "/media-files/"+itoa(uid.N(999))+"/probe", e.admin.token, nil, nil)
	e.expect(403, "POST", "/media-files/"+itoa(noDuration.ID)+"/probe", e.plain.token, nil, nil)
}
