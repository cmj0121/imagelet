//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// RealCollector collects system information from Linux proc/sys interfaces.
type RealCollector struct{}

// Collect gathers system information from standard Linux kernel interfaces.
func (r RealCollector) Collect() (*SysInfo, error) {
	info := &SysInfo{}
	var err error

	// Hostname
	info.Hostname, err = os.Hostname()
	if err != nil {
		info.Hostname = "unknown"
	}

	// OS name from /etc/os-release
	info.OSName = readOSRelease()

	// Kernel version from /proc/version (third word: "Linux version X.Y.Z-...")
	info.KernelVersion = readProcVersion()

	// CPU model from /proc/cpuinfo
	info.CPUModel = readCPUModel()

	// CPU cores
	info.CPUCores = runtime.NumCPU()

	// Total RAM from /proc/meminfo (MemTotal in kB × 1024)
	info.TotalRAMBytes = readMemTotal()

	// Uptime from /proc/uptime (first float in seconds)
	info.UptimeSeconds = readUptime()

	// Load averages from /proc/loadavg (first three fields)
	info.LoadAvg1, info.LoadAvg5, info.LoadAvg15 = readLoadAvg()

	return info, nil
}

func readOSRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			val := strings.TrimPrefix(line, "PRETTY_NAME=")
			// Strip surrounding quotes if present
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return runtime.GOOS
}

func readProcVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	// Format: "Linux version X.Y.Z-... (gcc ...) ..."
	// Third word (index 2) is the kernel version string.
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		return fields[2]
	}
	return ""
}

func readCPUModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Match "model name", "Model name", or "Hardware" keys
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "model name") || strings.HasPrefix(lower, "hardware") {
			// Strip leading tab and ": " prefix
			if idx := strings.Index(line, ": "); idx >= 0 {
				return strings.TrimSpace(line[idx+2:])
			}
		}
	}
	return ""
}

func readMemTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			// fields: ["MemTotal:", "<kB>", "kB"]
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					return kb * 1024
				}
			}
		}
	}
	return 0
}

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	// Format: "12345.67 23456.78"
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func readLoadAvg() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	// Format: "0.12 0.34 0.56 1/234 5678"
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

// ensure RealCollector satisfies the Collector interface at compile time.
var _ Collector = RealCollector{}
