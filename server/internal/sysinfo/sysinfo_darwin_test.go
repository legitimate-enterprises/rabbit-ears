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

// A host whose kernel lacks a memory counter still samples, without the
// memory in use.
func TestSampleWithoutAMemoryCounter(t *testing.T) {
	was := memoryCounters
	t.Cleanup(func() { memoryCounters = was })
	memoryCounters = append([]string{"vm.no_such_counter"}, was[1:]...)
	f, err := New().Sample(t.TempDir())
	if err != nil || f.MemoryUsed != 0 || f.MemoryTotal == 0 || f.DiskTotal == 0 {
		t.Fatalf("%+v %v", f, err)
	}
}
