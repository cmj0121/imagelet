// Package sysinfo exposes the /sysinfo service: GET /sysinfo returns
// current system information (hostname, OS, kernel, CPU, RAM, uptime,
// and load averages) rendered as a pylon banner with five borderless
// caption rows underneath.
//
// The headline is the fixed string "SYSINFO" — not the hostname — to
// avoid banner layout blowout on long cloud hostnames. The captions carry
// all the host-specific detail.
//
// The wire format is content-negotiated identically to /now:
//
//   - Accept: text/pylon OR ?format=pylon → raw pylon source
//   - ?format=html or User-Agent contains Mozilla → text/html (inline SVG)
//   - ?format=svg → image/svg+xml
//   - ?format=png → image/png
//   - ?format=ascii → text/plain; charset=utf-8 (ASCII)
//   - everything else → text/plain; charset=utf-8 (ASCII)
//
// System data is collected by a Collector implementation (platform-
// specific RealCollector or a test fake) and cached atomically every
// five seconds via startRefresher so the handler never blocks on syscalls.
// Cache-Control: max-age=5 is set on every response to match the refresh
// cadence.
package sysinfo

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// SysInfo holds a point-in-time snapshot of the host system state.
type SysInfo struct {
	Hostname      string
	OSName        string
	KernelVersion string
	CPUModel      string
	CPUCores      int
	TotalRAMBytes int64
	UptimeSeconds int64
	LoadAvg1      float64
	LoadAvg5      float64
	LoadAvg15     float64
}

// Collector is the interface for gathering system information.
// Register accepts any implementation; tests supply fakes, production
// code wires in RealCollector.
type Collector interface {
	Collect() (*SysInfo, error)
}

// Handler holds the gin HandlerFunc state: a Collector for background
// refreshes and an atomic pointer to the latest cached snapshot.
// Exported so callers can invoke Start(ctx) with the process shutdown context.
type Handler struct {
	c   Collector
	ptr atomic.Pointer[SysInfo]
}

// Register mounts GET /sysinfo on r using c as the system-info source.
// It performs an initial synchronous Collect so the first request is
// never served a nil snapshot. The returned *Handler must have Start(ctx)
// called by the caller (typically main.go) to launch the background
// refresher with the process shutdown context.
func Register(r gin.IRouter, c Collector) *Handler {
	h := &Handler{c: c}

	// Initial synchronous collection — ensures the pointer is non-nil
	// before any request arrives.
	if info, err := c.Collect(); err != nil {
		log.Warn().Err(err).Msg("sysinfo: initial collect failed; storing zero snapshot")
		h.ptr.Store(&SysInfo{Hostname: "unknown"})
	} else {
		h.ptr.Store(info)
	}

	r.GET("/sysinfo", h.ServeHTTP)
	return h
}

// Start launches the background cache-refresh goroutine that calls
// Collect every five seconds. The goroutine exits when ctx is cancelled,
// allowing clean shutdown. Call this once after Register, passing the
// process-level shutdown context (e.g. from signal.NotifyContext).
func (h *Handler) Start(ctx context.Context) {
	go startRefresher(ctx, h.c, &h.ptr)
}

// ServeHTTP responds with the cached system snapshot rendered through the
// standard pylon pipeline with Cache-Control: max-age=5.
func (h *Handler) ServeHTTP(c *gin.Context) {
	info := h.ptr.Load()
	if info == nil {
		info = &SysInfo{Hostname: "unknown"}
	}

	head := "SYSINFO"
	lines := captionLines(info)

	c.Header("Cache-Control", "max-age=5")

	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerSourceMulti(head, lines)))
		return
	}

	mode := middleware.ResolveMode(c)
	body, err := render.BannerMulti(head, lines, mode)
	if err != nil {
		log.Error().Err(err).Stringer("mode", mode).Msg("sysinfo: render banner")
		body, _ = render.BannerMulti(head, lines, render.ModeASCII)
		mode = render.ModeASCII
	}

	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	if mode == render.ModeSVG {
		c.Data(http.StatusOK, "image/svg+xml", body)
		return
	}
	if mode == render.ModeHTML {
		og := render.OGMeta{
			Title:       "SYSINFO · " + info.Hostname,
			Description: info.OSName + " " + info.KernelVersion,
			ImageURL:    middleware.AbsoluteURL(c, "format=png"),
			ImageType:   "image/png",
			PageURL:     middleware.AbsoluteURL(c, ""),
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// captionLines builds the five borderless caption rows displayed under the
// "SYSINFO" banner:
//
//  1. hostname line
//  2. OS + kernel line
//  3. CPU model + core count
//  4. RAM (human-readable)
//  5. Uptime + load average triple
//
// Each line is passed through pylonSafe to replace whitespace-only content
// with "—", preventing the pylon parser from panicking on empty captions
// (e.g. when the initial Collect fails and the zero-value snapshot is used).
func captionLines(info *SysInfo) []string {
	return []string{
		pylonSafe(info.Hostname),
		pylonSafe(info.OSName + " " + info.KernelVersion),
		pylonSafe(fmt.Sprintf("%s x%d", info.CPUModel, info.CPUCores)),
		"RAM " + formatRAM(info.TotalRAMBytes),
		"up " + formatUptime(info.UptimeSeconds) + "  load " + formatLoad(info.LoadAvg1, info.LoadAvg5, info.LoadAvg15),
	}
}

// pylonSafe returns s if it contains at least one non-space character,
// otherwise returns "—". The pylon parser panics on whitespace-only
// caption content, so callers must sanitize user-supplied or potentially
// empty strings before embedding them in a pylon source.
func pylonSafe(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// startRefresher runs in a goroutine, calling c.Collect every five seconds
// and storing the result in ptr. Errors are logged only on state transitions
// (ok→fail and fail→ok) to avoid flooding the log at 720 lines/hour on a
// sustained failure. The loop exits when ctx is cancelled.
func startRefresher(ctx context.Context, c Collector, ptr *atomic.Pointer[SysInfo]) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := c.Collect()
			if err != nil {
				if !failing {
					log.Warn().Err(err).Msg("sysinfo: refresh collect failed; keeping stale snapshot")
					failing = true
				}
				continue
			}
			if failing {
				log.Info().Msg("sysinfo: refresh collect recovered")
				failing = false
			}
			ptr.Store(info)
		}
	}
}

// formatRAM formats a byte count into a human-readable IEC string
// (e.g. "15.6 GiB", "512.0 MiB", "1023 B").
func formatRAM(bytes int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)
	switch {
	case bytes >= TiB:
		return fmt.Sprintf("%.1f TiB", float64(bytes)/float64(TiB))
	case bytes >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(GiB))
	case bytes >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(MiB))
	case bytes >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(bytes)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// formatUptime formats a duration in seconds to a human-readable string
// of the form "Xd Xh Xm". Leading zero components are omitted except
// that "0m" is always returned when all components are zero.
func formatUptime(seconds int64) string {
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60

	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// formatLoad formats three load-average floats as "%.2f %.2f %.2f".
func formatLoad(a, b, c float64) string {
	return fmt.Sprintf("%.2f %.2f %.2f", a, b, c)
}
