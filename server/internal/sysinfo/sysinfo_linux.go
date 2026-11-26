package sysinfo

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
)

func cpuModel() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	return cpuInfoModel(b)
}

// cpuInfoModel is the model x86 names in cpuinfo. ARM names none, so the
// architecture stands in.
func cpuInfoModel(b []byte) string {
	for line := range bytes.Lines(b) {
		if k, v, ok := strings.Cut(string(line), ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return runtime.GOARCH
}

func memoryTotal() uint64 {
	b, _ := os.ReadFile("/proc/meminfo")
	total, _, _ := memInfo(b)
	return total
}

func memoryUsed() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	_, used, err := memInfo(b)
	return used, err
}

// memInfo is the total and, as the kernel's MemAvailable leaves it, the
// used bytes of a meminfo reading.
func memInfo(b []byte) (total, used uint64, err error) {
	kb := map[string]uint64{}
	for line := range bytes.Lines(b) {
		f := strings.Fields(string(line))
		if len(f) >= 2 {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				kb[strings.TrimSuffix(f[0], ":")] = n
			}
		}
	}
	t, okT := kb["MemTotal"]
	a, okA := kb["MemAvailable"]
	if !okT || !okA {
		return t << 10, 0, errors.New("meminfo: no MemTotal or MemAvailable")
	}
	return t << 10, (t - min(a, t)) << 10, nil
}

// cpuMeter holds the previous /proc/stat reading. The kernel counts CPU
// time since boot, so the share is of the time between two readings; the
// first reading's is since boot.
type cpuMeter struct{ busy, total uint64 }

func (m *cpuMeter) read(int) (float64, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	return m.next(b)
}

func (m *cpuMeter) next(stat []byte) (float64, error) {
	line, _, _ := bytes.Cut(stat, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 9 || f[0] != "cpu" {
		return 0, errors.New("stat: no cpu line")
	}
	// user nice system idle iowait irq softirq steal; guest time is already
	// in user.
	var ticks [8]uint64
	for i := range ticks {
		n, err := strconv.ParseUint(f[i+1], 10, 64)
		if err != nil {
			return 0, err
		}
		ticks[i] = n
	}
	var total uint64
	for _, n := range ticks {
		total += n
	}
	busy := total - ticks[3] - ticks[4]
	dBusy, dTotal := busy-m.busy, total-m.total
	m.busy, m.total = busy, total
	if dTotal == 0 {
		return 0, nil
	}
	return min(100*float64(dBusy)/float64(dTotal), 100), nil
}
