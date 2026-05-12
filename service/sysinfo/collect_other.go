//go:build !linux && !darwin

package sysinfo

import (
	"os"
	"runtime"
)

// RealCollector is a stub implementation for platforms other than Linux
// and Darwin. It returns a minimal SysInfo with hostname and GOOS populated
// and zero values for everything else.
type RealCollector struct{}

// Collect returns a minimal SysInfo with hostname and runtime.GOOS populated.
func (r RealCollector) Collect() (*SysInfo, error) {
	hostname, _ := os.Hostname()
	return &SysInfo{
		Hostname: hostname,
		OSName:   runtime.GOOS,
		CPUCores: runtime.NumCPU(),
	}, nil
}

// ensure RealCollector satisfies the Collector interface at compile time.
var _ Collector = RealCollector{}
