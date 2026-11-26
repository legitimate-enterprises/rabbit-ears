package sysinfo

import "testing"

func TestCPUPercentFromPS(t *testing.T) {
	out := []byte("  0.0\n 12.5\n150.0\n\n bogus\n 37.5\n")
	if got := cpuPercent(out, 4); got != 50 {
		t.Fatalf("summed process CPU over cores: %v", got)
	}
	if got := cpuPercent([]byte("900\n"), 4); got != 100 {
		t.Fatalf("capped at 100: %v", got)
	}
}
