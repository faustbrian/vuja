//go:build darwin

package root

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	darwinHostVMInfo      = 2
	darwinHostCPULoadInfo = 3
)

type darwinCPULoad struct {
	Ticks [4]uint32
}

type darwinVMStatistics struct {
	Free     uint32
	Active   uint32
	Inactive uint32
	Wired    uint32
	_        [44]byte
}

var darwinMetrics = struct {
	sync.Mutex
	once           sync.Once
	err            error
	hostStatistics func(uint32, int32, uintptr, *uint32) int32
	machHostSelf   func() uint32
	previous       darwinCPULoad
	hasPrevious    bool
}{}

func sampleSystemUsage() (float64, float64) {
	darwinMetrics.Lock()
	defer darwinMetrics.Unlock()
	darwinMetrics.once.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			darwinMetrics.err = err
			return
		}
		purego.RegisterLibFunc(&darwinMetrics.hostStatistics, handle, "host_statistics")
		purego.RegisterLibFunc(&darwinMetrics.machHostSelf, handle, "mach_host_self")
	})
	if darwinMetrics.err != nil {
		return 0, 0
	}
	host := darwinMetrics.machHostSelf()
	var cpu darwinCPULoad
	cpuCount := uint32(len(cpu.Ticks))
	if darwinMetrics.hostStatistics(host, darwinHostCPULoadInfo, uintptr(unsafe.Pointer(&cpu)), &cpuCount) != 0 {
		return 0, 0
	}
	cpuUse := 0.0
	if darwinMetrics.hasPrevious {
		var previousTotal, previousIdle, total, idle uint64
		for index, ticks := range cpu.Ticks {
			total += uint64(ticks)
			previousTotal += uint64(darwinMetrics.previous.Ticks[index])
			if index == 2 {
				idle = uint64(ticks)
				previousIdle = uint64(darwinMetrics.previous.Ticks[index])
			}
		}
		cpuUse = cpuPercent(previousTotal, previousIdle, total, idle)
	}
	darwinMetrics.previous = cpu
	darwinMetrics.hasPrevious = true

	var memory darwinVMStatistics
	memoryCount := uint32(15)
	if darwinMetrics.hostStatistics(host, darwinHostVMInfo, uintptr(unsafe.Pointer(&memory)), &memoryCount) != 0 {
		return cpuUse, 0
	}
	total, totalErr := unix.SysctlUint64("hw.memsize")
	pageSize, pageErr := unix.SysctlUint64("hw.pagesize")
	if totalErr != nil || pageErr != nil {
		return cpuUse, 0
	}
	available := uint64(memory.Free+memory.Inactive) * pageSize
	if available >= total {
		return cpuUse, 0
	}
	return cpuUse, percent(float64(total-available), float64(total))
}
