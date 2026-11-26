package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInstanceNames(t *testing.T) {
	long := strings.Repeat("é", 100)
	for in, want := range map[string]string{"Den": "Den", "": fallbackName, "  \r\n": fallbackName, "Living\nroom": "Living room", long: long[:62]} {
		if got := instance(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// fakeDNSSD logs each registration's arguments and then lingers like dns-sd.
func fakeDNSSD(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "dns-sd"), filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexec sleep 30\n"
	//nolint:gosec // a fake tool must be executable
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// calls waits for at least want registrations and returns them.
func calls(t *testing.T, log string, want int) []string {
	t.Helper()
	for range 200 {
		b, _ := os.ReadFile(log) //nolint:gosec // test temp path
		if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) >= want && lines[0] != "" {
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fewer than %d registrations", want)
	return nil
}

func TestAdvertiseRegistersAndFollowsTheName(t *testing.T) {
	bin, log := fakeDNSSD(t)
	var mu sync.Mutex
	current := "Den"
	name := func() string { mu.Lock(); defer mu.Unlock(); return current }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Advertise(ctx, bin, 8484, name, 20*time.Millisecond) }()

	if got := calls(t, log, 1)[0]; got != "-R Den _rabbit-ears._tcp . 8484" {
		t.Fatalf("%q", got)
	}
	mu.Lock()
	current = "Living room"
	mu.Unlock()
	if got := calls(t, log, 2)[1]; got != "-R Living room _rabbit-ears._tcp . 8484" {
		t.Fatalf("%q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Advertise did not stop with its context")
	}
}

func TestAdvertiseWithoutTheToolReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		Advertise(context.Background(), "no-such-dns-sd", 1, func() string { return "" }, time.Hour)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("kept waiting for a tool that is not there")
	}
}
