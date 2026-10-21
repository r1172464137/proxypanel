//go:build windows

package telemetry

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel          = syscall.NewLazyDLL("kernel32.dll")
	procGetSysTimes = kernel.NewProc("GetSystemTimes")
	procGlobalMem   = kernel.NewProc("GlobalMemoryStatusEx")
	procDiskFree    = kernel.NewProc("GetDiskFreeSpaceExW")
)

type filetime struct {
	low  uint32
	high uint32
}

func (f filetime) value() uint64 { return uint64(f.high)<<32 | uint64(f.low) }

var (
	cpuMu    sync.Mutex
	lastCPUT time.Time
)

// cpuPercent measures the busy share between two calls.
func cpuPercent() (float64, bool) {
	var idle, kernelTime, user filetime
	r, _, _ := procGetSysTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, false
	}
	// kernelTime includes idle time on Windows.
	total := kernelTime.value() + user.value()
	busy := total - idle.value()
	cpuMu.Lock()
	defer cpuMu.Unlock()
	now := time.Now()
	if lastCPUT.IsZero() || !now.After(lastCPUT) || lastCPUValues == nil {
		lastCPUT = now
		lastCPUValues = []uint64{busy, total}
		return 0, false
	}
	prevBusy, prevTotal := lastCPUValues[0], lastCPUValues[1]
	lastCPUT = now
	lastCPUValues = []uint64{busy, total}
	if total <= prevTotal {
		return 0, false
	}
	deltaBusy := float64(busy - prevBusy)
	deltaTotal := float64(total - prevTotal)
	if deltaTotal <= 0 {
		return 0, false
	}
	return deltaBusy / deltaTotal * 100, true
}

var lastCPUValues []uint64

type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// memoryUsedPercent reports the physical memory usage percentage.
func memoryUsedPercent() (float64, bool) {
	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	r, _, _ := procGlobalMem.Call(uintptr(unsafe.Pointer(&status)))
	if r == 0 || status.totalPhys == 0 {
		return 0, false
	}
	return float64(status.totalPhys-status.availPhys) / float64(status.totalPhys) * 100, true
}

// diskUsedPercent reports the used share of the volume that holds dir.
func diskUsedPercent(dir string) (float64, bool) {
	if dir == "" {
		return 0, false
	}
	ptr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var freeAvail, total, totalFree uint64
	r, _, _ := procDiskFree.Call(uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeAvail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 || total == 0 {
		return 0, false
	}
	return float64(total-totalFree) / float64(total) * 100, true
}

// loadAverage1 is not available on Windows.
func loadAverage1() (float64, bool) { return 0, false }

// netBytes is not collected on Windows (no dependency free counter available).
func netBytes() (int64, int64, bool) { return 0, 0, false }
