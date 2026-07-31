//go:build linux

package root

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var linuxCPU = struct {
	sync.Mutex
	total uint64
	idle  uint64
	set   bool
}{}

func sampleSystemUsage() (float64, float64) {
	stat, _ := os.ReadFile("/proc/stat")
	line, _, _ := strings.Cut(string(stat), "\n")
	fields := strings.Fields(line)
	var total, idle uint64
	for index, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
		if index == 3 || index == 4 {
			idle += value
		}
	}
	linuxCPU.Lock()
	cpuUse := 0.0
	if linuxCPU.set {
		cpuUse = cpuPercent(linuxCPU.total, linuxCPU.idle, total, idle)
	}
	linuxCPU.total, linuxCPU.idle, linuxCPU.set = total, idle, true
	linuxCPU.Unlock()

	memoryRaw, _ := os.ReadFile("/proc/meminfo")
	var memoryTotal, available float64
	for _, line := range strings.Split(string(memoryRaw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseFloat(fields[1], 64)
		switch fields[0] {
		case "MemTotal:":
			memoryTotal = value
		case "MemAvailable:":
			available = value
		}
	}
	return cpuUse, percent(memoryTotal-available, memoryTotal)
}
