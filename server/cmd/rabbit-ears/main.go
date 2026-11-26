// Command rabbit-ears runs the Rabbit Ears media server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/discovery"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/gravatar"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/webui"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

const defaultPort = "8477"

// routes puts the API under its prefix and the web client everywhere else,
// so an unknown API path stays a JSON 404 instead of loading a page.
func routes(api http.Handler, site fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", webui.Handler(site))
	return mux
}

// listenAddr reads RABBIT_EARS_PORT; the default avoids 8096 (Jellyfin's).
func listenAddr() string {
	port := os.Getenv("RABBIT_EARS_PORT")
	if port == "" {
		port = defaultPort
	}
	return ":" + port
}

// dbPath reads RABBIT_EARS_DB, else uses the per-user application support
// folder.
func dbPath() (string, error) {
	if p := os.Getenv("RABBIT_EARS_DB"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Rabbit Ears", "rabbit-ears.db"), nil
}

// dataDir reads RABBIT_EARS_DATA, the home of the metadata cache, posters,
// chapter thumbnails and seek previews; it defaults beside the database.
func dataDir(dbFile string) string {
	if p := os.Getenv("RABBIT_EARS_DATA"); p != "" {
		return p
	}
	return filepath.Dir(dbFile)
}

// startScanning runs the scheduler and file watcher until ctx ends, and
// reports each finished loop on wg. While client has no TMDB key, scans
// skip TMDB, and while the metadata settings use no remote data they call
// no provider. The scheduler also runs stations, the hourly Schedule
// stations task, and tells events of each scan that changed its library.
func startScanning(ctx context.Context, wg *sync.WaitGroup, d *sql.DB, client *tmdb.Client, data string, stations func(context.Context) (string, error), events *live.Hub) error {
	if !client.Enabled() {
		logstore.Log("server").Info("No TMDB key is set; library scans will not fetch metadata until one is saved in Settings")
	}
	libs, info := library.New(d), serverinfo.New(d)
	sc := scan.New(scan.Config{Libraries: libs, Items: item.New(d), Files: mediafile.New(d), Editions: edition.New(d), Genres: genre.New(d),
		People: person.New(d), Provider: client,
		Collections: collection.New(d), Music: musicbrainz.New(musicbrainz.Config{CacheDir: filepath.Join(data, "cache", "musicbrainz")}),
		ImageDir: filepath.Join(data, "images"), ThumbDir: filepath.Join(data, "thumbnails"),
		Previews: seekpreview.New(d), PreviewDir: filepath.Join(data, "previews"), YouTube: youtube.New(d),
		Avatars: youtube.NewAvatars(filepath.Join(data, "cache", "youtube")), Settings: info, Reviews: review.New(d)})
	runner := scan.NewRunner(sc, task.New(d))
	runner.Stations, runner.Events = stations, events
	runner.Paused = func() bool {
		i, err := info.Get()
		return err == nil && i.TasksPaused
	}
	w, err := scan.NewWatcher(libs, func(id int64) {
		if err := runner.Trigger(id); err != nil {
			logstore.Log("scan").Warn("Watch trigger failed", "err", err)
		}
	}, 30*time.Second)
	if err != nil {
		return err
	}
	runner.Watch = w
	wg.Add(2)
	go func() { defer wg.Done(); w.Run(ctx) }()
	go func() { defer wg.Done(); runner.Run(ctx) }()
	return nil
}

// startDiscovery answers the UDP discovery datagram on addr, the HTTP
// listen address, until ctx ends. A port it cannot bind is logged and
// skipped: clients with Bonjour or a typed address still find the server.
func startDiscovery(ctx context.Context, wg *sync.WaitGroup, addr string, name func() string) {
	_, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		logstore.Log("server").Warn("Discovery is off: could not listen for UDP on "+addr, "err", err)
		return
	}
	wg.Add(1)
	subnets, named := discovery.Cached(time.Minute, playback.InterfaceSubnets), discovery.Cached(10*time.Second, name)
	local := func(ip net.IP) bool { return playback.SameLAN(ip, subnets(), true) }
	go func() { defer wg.Done(); discovery.Serve(ctx, conn, port, named, local) }()
}

// startBonjour advertises the HTTP port and server name as _rabbit-ears._tcp
// until ctx ends, for clients that browse instead of broadcasting.
func startBonjour(ctx context.Context, wg *sync.WaitGroup, addr string, name func() string) {
	_, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	wg.Add(1)
	go func() { defer wg.Done(); discovery.Advertise(ctx, "dns-sd", port, name, 10*time.Second) }()
}

// air tells the event stream, second by second, of the programs that came
// on or ended on any station.
// shortcut: reads the schedule every second, sleep to the next program's edge if that costs.
func air(ctx context.Context, s *service.StationService) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := s.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := s.Now()
			if err := s.Aired(last, now); err != nil {
				logstore.Log("server").Warn("Could not announce what is on air", "err", err)
			}
			last = now
		}
	}
}

// reap ends sessions without a position report for staleAfter, freeing
// their encoders and transcode slots.
func reap(ctx context.Context, p *service.PlaybackService) {
	const staleAfter = 30 * time.Minute
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Reap(time.Now().Add(-staleAfter)); err != nil {
				logstore.Log("playback").Warn("Could not reap stale sessions", "err", err)
			}
		}
	}
}

// clean removes expired downloads and the conversions and files nobody
// needs, hourly, whether or not a conversion is being made.
func clean(ctx context.Context, d *service.DownloadService) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Clean(time.Now()); err != nil {
				logstore.Log("transcode").Error("Downloads not cleaned up", "err", err)
			}
		}
	}
}

// resetPassword gives the adult with the email in args a temporary password
// and prints it: the way back in for an admin locked out of every device.
func resetPassword(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: rabbit-ears reset-password <email>")
	}
	path, err := dbPath()
	if err != nil {
		return err
	}
	d, err := db.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(d); err != nil {
		return err
	}
	a, err := account.New(d).ByEmail(args[0])
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("no account has the email %s", args[0])
	}
	if err != nil {
		return err
	}
	temp := service.TemporaryPassword()
	if _, err := service.NewAuthService(d).SetPassword(a, temp, 0); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Temporary password for %s: %s\nSign in with it, then choose a new one under Password.\n", a.Name, temp)
	return err
}

// newServer serves the configured address. ReadTimeout bounds reading a
// request, body included, and never an outgoing stream. No WriteTimeout: it
// would cut films served whole. Every request's context ends with ctx, so
// held polls answer at shutdown instead of holding the drain to its deadline.
func newServer(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{Addr: listenAddr(), Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
}

// shutdownTimeout bounds how long open requests get to finish after a signal.
const shutdownTimeout = 5 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		if err := resetPassword(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "rabbit-ears reset-password:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("rabbit-ears stopped", "err", err)
		os.Exit(1)
	}
}

// run serves until SIGINT or SIGTERM, then stops accepting, drains requests,
// stops the scheduler, watcher and discovery responder, kills running
// encoders and closes the database, in that order.
func run() error {
	// The port is the one-instance lock: taken first, so a second start
	// fails before it kills the running server's encoders or clears its
	// caches.
	ln, err := net.Listen("tcp", listenAddr())
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	path, err := dbPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	d, err := db.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(d); err != nil {
		return err
	}
	if err := occasion.New(d).SeedBuiltIns(); err != nil {
		return err
	}
	logs := logstore.NewWriter(logstore.New(d))
	// Deferred after the database's close, so it flushes first.
	defer logs.Close()
	slog.SetDefault(slog.New(logstore.NewHandler(slog.NewTextHandler(os.Stderr, nil), logs, slog.LevelInfo)))
	log := logstore.Log("server")

	svc := service.New(d)
	data := dataDir(path)
	db.DataDir = data
	// Encoders, conversions' among them, never outlive the process that
	// started them, nor do segments.
	cache := filepath.Join(data, "transcode")
	svc.Downloads.Dir = filepath.Join(data, "cache", "downloads")
	for _, dir := range []string{cache, svc.Downloads.Dir} {
		if err := transcode.KillOrphans(dir); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(cache); err != nil {
		return err
	}
	maxVideo, cacheCap, err := svc.Server.TranscodeLimits()
	if err != nil {
		return err
	}
	svc.Playback.Engine = transcode.New(transcode.Config{Root: cache, MaxVideo: maxVideo, CacheCap: cacheCap})
	client := tmdb.New(tmdb.Config{CacheDir: filepath.Join(data, "cache", "tmdb")})
	svc.DataDir, svc.TMDB.Client, svc.TMDB.FallbackKey = data, client, os.Getenv("RABBIT_EARS_TMDB_KEY")
	svc.Port, svc.DatabasePath = ln.Addr().(*net.TCPAddr).Port, path
	if err := svc.TMDB.Load(); err != nil {
		return err
	}
	svc.Items.Images, svc.Items.ImageDir = client, filepath.Join(data, "images")
	svc.People.Images, svc.People.ImageDir = client, svc.Items.ImageDir
	svc.Avatars = gravatar.New(filepath.Join(data, "cache", "gravatar"))
	svc.Channels.IdentDir = filepath.Join(data, "cache", "idents")
	defer svc.Playback.Engine.Close()
	defer svc.Channels.Close()
	// The server and its API are one release with one version; the spec
	// states it.
	doc, err := api.GetSpec()
	if err != nil {
		return err
	}
	version := doc.Info.Version
	h, err := httpapi.NewHandler(version, svc)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var bg sync.WaitGroup
	bg.Add(4)
	go func() { defer bg.Done(); reap(ctx, svc.Playback) }()
	go func() { defer bg.Done(); clean(ctx, svc.Downloads) }()
	go func() { defer bg.Done(); air(ctx, svc.Stations) }()
	go func() { defer bg.Done(); svc.Downloads.Run(ctx) }()
	if err := startScanning(ctx, &bg, d, client, data, svc.Channels.Schedule, svc.Events); err != nil {
		stop()
		bg.Wait()
		return err
	}
	serverName := func() string {
		info, err := svc.Server.Get()
		if err != nil {
			return ""
		}
		return info.Name
	}
	startDiscovery(ctx, &bg, listenAddr(), serverName)
	startBonjour(ctx, &bg, listenAddr(), serverName)
	srv := newServer(ctx, routes(h, webui.Site()))
	log.Info("rabbit-ears "+version+" listening on "+srv.Addr, "database", path)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err = <-served:
		stop()
	case <-ctx.Done():
		log.Info("Shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err = srv.Shutdown(sctx); err != nil {
			_ = srv.Close() // streams still open after the deadline are cut
		}
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	}
	bg.Wait()
	return err
}
