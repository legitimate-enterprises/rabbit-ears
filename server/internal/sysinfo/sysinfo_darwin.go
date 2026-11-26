package sysinfo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

func cpuModel() string {
	m, _ := unix.Sysctl("machdep.cpu.brand_string")
	return m
}

func memoryTotal() uint64 {
	n, _ := unix.SysctlUint64("hw.memsize")
	return n
}

// cpuMeter reads ps, whose per-process CPU already decays over recent
// seconds.
type cpuMeter struct{}

func (cpuMeter) read(cores int) (float64, error) {
	out, err := exec.Command("ps", "-A", "-o", "%cpu=").Output()
	return cpuPercent(out, cores), err
}

// cpuPercent turns ps's per-process CPU, where one busy core is 100, into
// the share of the whole machine.
func cpuPercent(ps []byte, cores int) float64 {
	var sum float64
	for _, line := range bytes.Split(ps, []byte("\n")) {
		if v, err := strconv.ParseFloat(string(bytes.TrimSpace(line)), 64); err == nil {
			sum += v
		}
	}
	return min(sum/float64(max(cores, 1)), 100)
}

// memoryUsed is app memory (anonymous pages less purgeable) plus wired.
func memoryUsed() (uint64, error) {
	var pages [4]uint64
	for i, name := range []string{"vm.pagesize", "vm.page_pageable_internal_count", "vm.page_purgeable_count", "vm.page_wired_count"} {
		// These are 32 or 64 bits wide depending on the name and OS release.
		b, err := unix.SysctlRaw(name)
		switch {
		case err != nil:
			return 0, err
		case len(b) == 8:
			pages[i] = binary.LittleEndian.Uint64(b)
		case len(b) == 4:
			pages[i] = uint64(binary.LittleEndian.Uint32(b))
		default:
			return 0, fmt.Errorf("sysctl %s: unexpected width %d", name, len(b))
		}
	}
	size, internal, purgeable, wired := pages[0], pages[1], pages[2], pages[3]
	return (internal - min(purgeable, internal) + wired) * size, nil
}
