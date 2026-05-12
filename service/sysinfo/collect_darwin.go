//go:build darwin

package sysinfo

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RealCollector collects system information via macOS sysctl interfaces.
type RealCollector struct{}

// Collect gathers system information using macOS sysctl calls.
func (r RealCollector) Collect() (*SysInfo, error) {
	info := &SysInfo{}

	// Hostname
	var err error
	info.Hostname, err = os.Hostname()
	if err != nil {
		info.Hostname = "unknown"
	}

	// OS name: "Darwin <release>"
	osType, _ := syscall.Sysctl("kern.ostype")
	osRelease, _ := syscall.Sysctl("kern.osrelease")
	info.OSName = osType + " " + osRelease

	// Kernel version: first line of kern.version
	kernVersion, _ := syscall.Sysctl("kern.version")
	if idx := strings.IndexByte(kernVersion, '\n'); idx >= 0 {
		kernVersion = kernVersion[:idx]
	}
	info.KernelVersion = strings.TrimSpace(kernVersion)

	// CPU model — try sysctl machdep.cpu.brand_string (x86_64 / Apple silicon
	// Rosetta). On native arm64, fall back to hw.model.
	info.CPUModel, err = syscall.Sysctl("machdep.cpu.brand_string")
	if err != nil || strings.TrimSpace(info.CPUModel) == "" {
		info.CPUModel, _ = syscall.Sysctl("hw.model")
	}

	// CPU cores
	info.CPUCores = runtime.NumCPU()

	// Total RAM: hw.memsize is a 64-bit integer. The stdlib syscall package
	// only exposes SysctlUint32, so we read it via `sysctl -n hw.memsize`.
	if memStr, err2 := sysctlN("hw.memsize"); err2 == nil {
		if v, err3 := strconv.ParseInt(memStr, 10, 64); err3 == nil {
			info.TotalRAMBytes = v
		}
	}

	// Uptime: derive from kern.boottime.
	info.UptimeSeconds = darwinUptime()

	// Load averages: vm.loadavg returns "{ 0.25 0.50 0.75 }" (pre-scaled).
	info.LoadAvg1, info.LoadAvg5, info.LoadAvg15 = darwinLoadAvg()

	return info, nil
}

// sysctlN runs `sysctl -n <key>` and returns the trimmed output.
func sysctlN(key string) (string, error) {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// darwinUptime returns seconds since boot. It first tries to decode the
// kern.boottime sysctl directly (which the stdlib exposes as a string),
// then parses the output of `sysctl -n kern.boottime`.
func darwinUptime() int64 {
	// `sysctl -n kern.boottime` prints:
	//   { sec = 1745600000, usec = 123456 } Tue Apr 25 12:00:00 2025
	out, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return 0
	}
	s := string(out)
	// Extract the `sec = <value>` field.
	const prefix = "sec = "
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexAny(rest, ", }")
	if end >= 0 {
		rest = rest[:end]
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil {
		return 0
	}
	return time.Now().Unix() - sec
}

// darwinLoadAvg reads vm.loadavg. The command `sysctl -n vm.loadavg` returns
// something like "{ 0.25 0.50 0.75 }" (already divided by fscale).
func darwinLoadAvg() (float64, float64, float64) {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, 0, 0
	}
	s := strings.TrimSpace(string(out))
	// Strip surrounding "{ " and " }"
	s = strings.Trim(s, "{ }")
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(v string) float64 {
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

// ensure RealCollector satisfies the Collector interface at compile time.
var _ Collector = RealCollector{}
