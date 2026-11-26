// Package sysinfo reads the host's hardware and load for the admin
// Settings and Dashboard. Each supported operating system supplies the
// readings in its own file: cpuModel, memoryTotal, memoryUsed and a
// cpuMeter.
package sysinfo

import (
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

// Facts is one reading of the host. A reading the host does not offer is
// zero.
type Facts struct {
	CPUModel     string
	Cores        int
	CPUPercent   float64 // whole machine, 0 to 100
	MemoryUsed   uint64  // bytes held by apps and the kernel: as Activity Monitor counts it without compression on macOS, all but MemAvailable on Linux
	MemoryTotal  uint64
	DiskFree     uint64 // of the volume holding the path sampled
	DiskTotal    uint64
	VideoToolbox bool // ffmpeg offers the VideoToolbox hardware encoders
}

// Sampler reads Facts for the volume holding path. Only that volume being
// unreadable is an error.
type Sampler interface {
	Sample(path string) (Facts, error)
}

// cpuEvery bounds how often the CPU is read however often the Dashboard
// polls.
const cpuEvery = 5 * time.Second

// New returns the sampler of this host's operating system.
func New() Sampler { return &sampler{} }

type sampler struct {
	once   sync.Once
	static Facts

	mu    sync.Mutex
	meter cpuMeter
	cpu   float64
	cpuAt time.Time
}

func (d *sampler) Sample(path string) (Facts, error) {
	d.once.Do(func() {
		d.static.CPUModel = cpuModel()
		d.static.Cores = runtime.NumCPU()
		d.static.MemoryTotal = memoryTotal()
		d.static.VideoToolbox = transcode.Hardware()
	})
	f := d.static
	f.CPUPercent = d.cpuPercent()
	f.MemoryUsed, _ = memoryUsed()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return f, err
	}
	f.DiskFree, f.DiskTotal = st.Bavail*uint64(st.Bsize), st.Blocks*uint64(st.Bsize) //nolint:gosec // a block size is positive
	return f, nil
}

func (d *sampler) cpuPercent() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.cpuAt) >= cpuEvery {
		d.cpu, _ = d.meter.read(d.static.Cores)
		d.cpuAt = time.Now()
	}
	return d.cpu
}
