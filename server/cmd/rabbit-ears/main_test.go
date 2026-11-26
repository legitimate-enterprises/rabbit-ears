package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/discovery"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

func TestListenAddr(t *testing.T) {
	cases := []struct {
		env, want string
	}{
		{"", ":8477"},
		{"9000", ":9000"},
	}
	for _, c := range cases {
		t.Setenv("RABBIT_EARS_PORT", c.env)
		if got := listenAddr(); got != c.want {
			t.Errorf("env %q: got %q, want %q", c.env, got, c.want)
		}
	}
}

func TestDBPath(t *testing.T) {
	t.Setenv("RABBIT_EARS_DB", "/tmp/x.db")
	if got, err := dbPath(); err != nil || got != "/tmp/x.db" {
		t.Fatalf("%q %v", got, err)
	}
	t.Setenv("RABBIT_EARS_DB", "")
	if got, err := dbPath(); err != nil || !strings.HasSuffix(got, "/Rabbit Ears/rabbit-ears.db") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestDataDir(t *testing.T) {
	t.Setenv("RABBIT_EARS_DATA", "")
	if got := dataDir("/a/b/x.db"); got != "/a/b" {
		t.Errorf("%q", got)
	}
	t.Setenv("RABBIT_EARS_DATA", "/data")
	if got := dataDir("/a/b/x.db"); got != "/data" {
		t.Errorf("%q", got)
	}
}

// TestHelperServer is the server itself, started by the shutdown test in a
// child process.
func TestHelperServer(t *testing.T) {
	if os.Getenv("RABBIT_EARS_TEST_SERVER") != "1" {
		t.Skip("child process only")
	}
	main()
}

// serve starts the server in a child process over a database and data in
// dir, and returns it with its exit and address once it answers.
func serve(t *testing.T, dir string) (*exec.Cmd, <-chan error, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	//nolint:gosec // re-runs this test binary
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperServer$")
	cmd.Env = append(os.Environ(), "RABBIT_EARS_TEST_SERVER=1", "RABBIT_EARS_DB="+filepath.Join(dir, "t.db"),
		"RABBIT_EARS_PORT="+itoa(port), "RABBIT_EARS_TMDB_KEY=")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// The port is taken before startup ends; an answer means serving.
	addr := "127.0.0.1:" + itoa(port)
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if res, err := http.Get("http://" + addr + "/api/v1/health"); err == nil {
			_ = res.Body.Close()
			return cmd, exited, addr
		}
		if time.Now().After(deadline) {
			t.Fatal("server never listened")
		}
	}
}

func TestShutsDownCleanlyOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd, exited, addr := serve(t, t.TempDir())
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("still running 10 s after the signal")
			}
			if c, err := net.Dial("tcp", addr); err == nil {
				_ = c.Close()
				t.Fatal("still listening")
			}
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A conversion's encoder left by a server that died, as one stopped for a
// live transcode is, goes when the next server starts.
func TestStartKillsAnOrphanedConversion(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "cache", "downloads", "1.mp4.part")
	if err := os.MkdirAll(filepath.Dir(part), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := exec.Command("tail", "-f", part) //nolint:gosec // temp path
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	gone := make(chan struct{})
	go func() { _ = orphan.Wait(); close(gone) }()
	t.Cleanup(func() { _ = orphan.Process.Kill(); <-gone })
	if err := orphan.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	serve(t, dir)
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphaned conversion survived the next start")
	}
}

// A second start while the server runs, as a mistyped command does, fails
// on the taken port before it touches the running server's encoders or
// segments.
func TestASecondStartLeavesTheRunningServerAlone(t *testing.T) {
	//nolint:gosec // the port is held as the server binds it, on every interface
	taken, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	dir := t.TempDir()
	t.Setenv("RABBIT_EARS_DB", filepath.Join(dir, "t.db"))
	t.Setenv("RABBIT_EARS_DATA", "")
	t.Setenv("RABBIT_EARS_PORT", itoa(taken.Addr().(*net.TCPAddr).Port))
	t.Setenv("RABBIT_EARS_TMDB_KEY", "")
	bin := t.TempDir()
	//nolint:gosec // a fake tool must be executable
	if err := os.WriteFile(filepath.Join(bin, "dns-sd"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	seg := filepath.Join(dir, "transcode", "1", "0-0", "0.m4s")
	if err := os.MkdirAll(filepath.Dir(seg), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	encoder := exec.Command("tail", "-f", seg) //nolint:gosec // temp path
	if err := encoder.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = encoder.Wait(); close(exited) }()
	t.Cleanup(func() { _ = encoder.Process.Kill(); <-exited })
	defer slog.SetDefault(slog.Default())

	if err := run(); err == nil {
		t.Fatal("a second server started on a taken port")
	}
	if _, err := os.Stat(seg); err != nil {
		t.Fatalf("the running server's segments are gone: %v", err)
	}
	select {
	case <-exited:
		t.Fatal("the running server's encoder was killed")
	case <-time.After(500 * time.Millisecond):
	}
}

// An admin locked out of every device recovers from the server's shell.
func TestResetPasswordPrintsALink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "re.db")
	t.Setenv("RABBIT_EARS_DB", path)
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	a := &account.Account{Kind: account.Adult, Email: "ada@example.test", Name: "Ada", IsAdmin: true, PasswordHash: "x"}
	if err := account.New(d).Create(a); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	var out bytes.Buffer
	if err := resetPassword([]string{"ADA@example.test"}, &out); err != nil {
		t.Fatal(err)
	}
	temp := regexp.MustCompile(`[2-9A-Z]{4}-[2-9A-Z]{4}-[2-9A-Z]{4}`).FindString(out.String())
	if temp == "" {
		t.Fatalf("no temporary password in %q", out.String())
	}
	d, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := service.NewAuthService(d).SignIn("", "ada@example.test", temp, "Mac", "macos"); err != nil {
		t.Fatalf("the printed password does not sign in: %v", err)
	}
	for _, args := range [][]string{nil, {"nobody@example.test"}} {
		if err := resetPassword(args, &out); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

func TestDiscoveryAnswersOnTheHTTPPortAndABindFailureIsNotFatal(t *testing.T) {
	taken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	startDiscovery(ctx, &wg, taken.LocalAddr().String(), func() string { return "Den" })
	wg.Wait() // nothing was started

	free, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := free.LocalAddr().String()
	_ = free.Close()
	startDiscovery(ctx, &wg, addr, func() string { return "Den" })
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte(discovery.Request))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 256)
	n, err := c.Read(b)
	_, port, _ := net.SplitHostPort(addr)
	if err != nil || string(b[:n]) != "RABBIT-EARS SERVER 1 "+port+" Den" {
		t.Fatalf("%q %v", b[:n], err)
	}
	cancel()
	wg.Wait()
}

func TestRoutesSplitTheAPIFromTheWebClient(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := routes(api, fstest.MapFS{"index.html": {Data: []byte("viewer")}})
	for path, want := range map[string]int{"/api/v1/health": 418, "/api/v9/nope": 418, "/": 200, "/link": 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}
}

// The shutdown signal ends the requests waiting on their context, as every
// connected client's held event poll does, so draining does not wait them
// out.
func TestShutdownEndsHeldRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	held := make(chan struct{})
	srv := newServer(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(held)
		<-r.Context().Done()
		w.WriteHeader(http.StatusNoContent)
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	answered := make(chan int, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			answered <- 0
			return
		}
		_ = res.Body.Close()
		answered <- res.StatusCode
	}()
	<-held
	cancel()
	sctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := srv.Shutdown(sctx); err != nil {
		t.Fatalf("draining waited out a held request: %v", err)
	}
	if code := <-answered; code != http.StatusNoContent {
		t.Fatalf("the held request was cut, not answered: %d", code)
	}
}

// A slow body upload is cut off, but nothing bounds how long a response may
// stream: a film served whole outlasts any write timeout.
func TestServerTimeouts(t *testing.T) {
	srv := newServer(context.Background(), http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout != 0 {
		t.Fatalf("header %v read %v write %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout)
	}
	srv.ReadTimeout = 200 * time.Millisecond
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 6 {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	res, err := http.Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var b bytes.Buffer
	if _, err := b.ReadFrom(res.Body); err != nil || b.Len() != 6 {
		t.Fatalf("%d bytes, %v", b.Len(), err)
	}

	srv2 := newServer(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := b.ReadFrom(r.Body)
		if err == nil {
			w.WriteHeader(200)
		}
	}))
	srv2.ReadTimeout = 200 * time.Millisecond
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv2.Serve(ln2) }()
	defer func() { _ = srv2.Close() }()
	c, err := net.Dial("tcp", ln2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nab"))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()
	buf := make([]byte, 512)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a stalled body held the connection %v", d)
	}
}

// The server registers _rabbit-ears._tcp with its HTTP port and name through
// dns-sd, and stops it on shutdown.
func TestBonjourAdvertisesThePortAndName(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" > " + log + "\nexec sleep 30\n"
	//nolint:gosec // a fake tool must be executable
	if err := os.WriteFile(filepath.Join(dir, "dns-sd"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	startBonjour(ctx, &wg, ":9123", func() string { return "Den" })
	var got []byte
	for range 200 {
		if got, _ = os.ReadFile(log); len(got) > 0 { //nolint:gosec // test temp path
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(got) != "-R Den _rabbit-ears._tcp . 9123\n" {
		t.Fatalf("%q", got)
	}
	cancel()
	wg.Wait()
}
