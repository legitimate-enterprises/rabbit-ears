package sysinfo

import (
	"runtime"
	"testing"
)

func TestCPUModelFromCPUInfo(t *testing.T) {
	x86 := []byte("processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz\n")
	if got := cpuInfoModel(x86); got != "Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz" {
		t.Errorf("x86 names its model: %q", got)
	}
	arm := []byte("processor\t: 0\nBogoMIPS\t: 48.00\nCPU implementer\t: 0x61\n")
	if got := cpuInfoModel(arm); got != runtime.GOARCH {
		t.Errorf("ARM names none, so the architecture stands in: %q", got)
	}
}

func TestMemoryFromMemInfo(t *testing.T) {
	total, used, err := memInfo([]byte("MemTotal:       16424480 kB\nMemFree:          100000 kB\nMemAvailable:   15561064 kB\n"))
	if err != nil || total != 16424480<<10 || used != (16424480-15561064)<<10 {
		t.Fatalf("used is what the kernel could not hand out: %d %d %v", total, used, err)
	}
	if _, _, err := memInfo([]byte("MemTotal: 1 kB\n")); err == nil {
		t.Fatal("a reading without MemAvailable is an error")
	}
}

// The share is of the time between two readings, idle and I/O wait not
// counting as busy.
func TestCPUPercentBetweenReadings(t *testing.T) {
	var m cpuMeter
	if _, err := m.next([]byte("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 100 0 50 800 50 0 0 0 0 0\n")); err != nil {
		t.Fatal(err)
	}
	got, err := m.next([]byte("cpu  200 0 100 840 60 0 0 0 0 0\n"))
	if err != nil || got != 75 {
		t.Fatalf("150 busy of 200 ticks: %v %v", got, err)
	}
	if _, err := m.next([]byte("intr 1 2 3\n")); err == nil {
		t.Fatal("a reading without the cpu line is an error")
	}
}
