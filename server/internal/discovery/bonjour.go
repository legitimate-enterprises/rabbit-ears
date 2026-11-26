package discovery

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
)

// Service is the Bonjour type the server advertises, with the HTTP port in
// the record and the server name as the instance name.
const Service = "_rabbit-ears._tcp"

// fallbackName stands in before first run, when the server has no name.
const fallbackName = "Rabbit Ears"

// maxInstance is the longest a DNS-SD instance name may be, in bytes.
const maxInstance = 63

// instance turns a server name into a valid instance name.
func instance(name string) string {
	name = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(name))
	if name == "" {
		return fallbackName
	}
	for len(name) > maxInstance {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// Advertise registers the server over Bonjour through the platform's
// dns-sd tool until ctx ends. The registration lives as long as the tool
// runs, so it is restarted when it exits and, every interval, when the
// server name has changed. A missing tool is logged and leaves the UDP
// responder and typed addresses.
func Advertise(ctx context.Context, bin string, port int, name func() string, every time.Duration) {
	path, err := exec.LookPath(bin)
	if err != nil {
		logstore.Log("discovery").Warn("Bonjour is off: dns-sd was not found", "err", err)
		return
	}
	for ctx.Err() == nil {
		register(ctx, path, port, instance(name()), name, every)
		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}

// register runs one dns-sd registration of n until it exits, ctx ends or
// the name changes.
func register(ctx context.Context, path string, port int, n string, name func() string, every time.Duration) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	//nolint:gosec // path is dns-sd and the arguments are a server name and a port
	cmd := exec.CommandContext(rctx, path, "-R", n, Service, ".", strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		logstore.Log("discovery").Warn("Bonjour registration failed to start", "err", err)
		return
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-exited:
			return
		case <-t.C:
			if instance(name()) != n {
				cancel()
				<-exited
				return
			}
		case <-ctx.Done():
			<-exited
			return
		}
	}
}
