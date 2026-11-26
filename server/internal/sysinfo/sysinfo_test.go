package sysinfo

import "testing"

func TestSampleReadsThisMachine(t *testing.T) {
	f, err := New().Sample(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if f.CPUModel == "" || f.Cores < 1 || f.MemoryTotal == 0 || f.MemoryUsed > f.MemoryTotal ||
		f.DiskTotal == 0 || f.DiskFree > f.DiskTotal || f.CPUPercent < 0 || f.CPUPercent > 100 {
		t.Fatalf("%+v", f)
	}
}
