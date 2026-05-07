package resolver

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/foxcpp/go-mockdns"
	"github.com/miekg/dns"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// startMock spins up a mockdns server with the given zones and returns
// it + a Client wired to its address with Net="tcp" (mockdns does NOT
// terminate TLS).
func startMock(t *testing.T, zones map[string]mockdns.Zone) (*mockdns.Server, *Client) {
	t.Helper()
	srv, err := mockdns.NewServer(zones, false)
	if err != nil {
		t.Fatalf("mockdns: %v", err)
	}
	srv.Log = quietLogger{}
	t.Cleanup(func() { _ = srv.Close() })

	c := NewWithEndpoint(srv.LocalAddr().String(), &dns.Client{
		Net:     "tcp",
		Timeout: 1 * time.Second,
	})
	return srv, c
}

// quietLogger discards mockdns's chatty trace output.
type quietLogger struct{}

func (quietLogger) Printf(format string, args ...any) {}

// caaRR builds a minimal CAA record under name (FQDN) for Misc-served
// zones. mockdns doesn't natively serve CAA, so zones use rzone.Misc.
func caaRR(name string, flag uint8, tag, value string) dns.RR {
	return &dns.CAA{
		Hdr:   dns.RR_Header{Name: name, Rrtype: dns.TypeCAA, Class: dns.ClassINET, Ttl: 60},
		Flag:  flag,
		Tag:   tag,
		Value: value,
	}
}

// fullExampleZones is the populated-everything fixture used by
// TestResolver_OK and TestResolver_DNSSECADBit_OR.
func fullExampleZones() map[string]mockdns.Zone {
	return map[string]mockdns.Zone{
		"example.com.": {
			AD:   true,
			A:    []string{"93.184.216.34"},
			AAAA: []string{"2606:2800:220:1:248:1893:25c8:1946"},
			TXT:  []string{"v=spf1 include:_spf.example.com ~all"},
			MX:   []net.MX{{Host: "mail.example.com.", Pref: 10}},
			NS:   []net.NS{{Host: "ns1.example.com."}, {Host: "ns2.example.com."}},
			SRV:  []net.SRV{{Target: "sip.example.com.", Port: 5060, Priority: 10, Weight: 50}},
			Misc: map[dns.Type][]dns.RR{
				dns.Type(dns.TypeCAA): {caaRR("example.com.", 0, "issue", "letsencrypt.org")},
			},
		},
	}
}

// ---------------------------------------------------------------------
// canonicalize / classifyTXT (unit tests on internal helpers)
// ---------------------------------------------------------------------

func TestResolver_LookupCanonicalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"EXAMPLE.COM.", "example.com"},
		{"  Example.Com  ", "example.com"},
		{"exämple.com", "xn--exmple-cua.com"},
	}
	for _, tc := range cases {
		got, err := canonicalize(tc.in)
		if err != nil {
			t.Errorf("canonicalize(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("canonicalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolver_TXTPerRRClassification(t *testing.T) {
	// Apex TXT = 1 SPF + 3 google-site-verification + 1 stripe-verification
	// + 1 raw "blah" → HasSPF=true, VerificationCount=4, OtherCount=1.
	rrs := []string{
		"v=spf1 include:_spf.google.com ~all",
		"google-site-verification=abc",
		"google-site-verification=def",
		"google-site-verification=ghi",
		"stripe-verification=foo",
		"hello world",
	}
	var sum TXTSummary
	for _, s := range rrs {
		classifyTXT(s, &sum)
	}
	if !sum.HasSPF {
		t.Error("HasSPF = false, want true")
	}
	if sum.VerificationCount != 4 {
		t.Errorf("VerificationCount = %d, want 4", sum.VerificationCount)
	}
	if sum.OtherCount != 1 {
		t.Errorf("OtherCount = %d, want 1", sum.OtherCount)
	}
	if sum.HasDMARC || sum.DKIMCount != 0 {
		t.Errorf("DMARC/DKIM unexpectedly set: %+v", sum)
	}
}

// ---------------------------------------------------------------------
// Lookup happy path
// ---------------------------------------------------------------------

func TestResolver_OK(t *testing.T) {
	_, c := startMock(t, fullExampleZones())

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rec.Hostname != "example.com" {
		t.Errorf("Hostname = %q, want example.com", rec.Hostname)
	}
	if len(rec.A) != 1 || rec.A[0].String() != "93.184.216.34" {
		t.Errorf("A = %v, want [93.184.216.34]", rec.A)
	}
	if len(rec.AAAA) != 1 {
		t.Errorf("AAAA = %v, want 1 entry", rec.AAAA)
	}
	if len(rec.NS) != 2 {
		t.Errorf("NS = %v, want 2 entries", rec.NS)
	}
	if len(rec.MX) != 1 || rec.MX[0].Host != "mail.example.com" {
		t.Errorf("MX = %+v, want one mail.example.com", rec.MX)
	}
	if len(rec.CAA) != 1 || rec.CAA[0].Tag != "issue" {
		t.Errorf("CAA = %+v, want one issue=letsencrypt.org", rec.CAA)
	}
	if len(rec.SRV) != 1 {
		t.Errorf("SRV = %+v, want 1 entry", rec.SRV)
	}
	if !rec.TXT.HasSPF {
		t.Errorf("TXT = %+v, want HasSPF=true", rec.TXT)
	}
	if !rec.DNSSECVerified {
		t.Errorf("DNSSECVerified = false, want true (zones AD=true)")
	}
	if rec.MinTTL <= 0 {
		t.Errorf("MinTTL = %v, want >0", rec.MinTTL)
	}
}

// ---------------------------------------------------------------------
// NXDOMAIN → ErrNotFound
// ---------------------------------------------------------------------

func TestResolver_NXDOMAIN(t *testing.T) {
	_, c := startMock(t, map[string]mockdns.Zone{})
	_, err := c.Lookup(context.Background(), "nonexistent.example.")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------
// pickResolver: all fail → ErrUnavailable
// ---------------------------------------------------------------------

func TestResolver_AllResolversFail_Unavailable(t *testing.T) {
	// Construct a Client whose resolver list points to a closed port.
	closed := pickClosedPort(t)
	c := NewWithEndpoint(closed, &dns.Client{Net: "tcp", Timeout: 100 * time.Millisecond})

	_, err := c.Lookup(context.Background(), "example.com")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// pickClosedPort returns a host:port that is closed. It opens then
// immediately closes a listener so the port is reliably free.
func pickClosedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// ---------------------------------------------------------------------
// Fallback: first preflight refused → second succeeds
// ---------------------------------------------------------------------

func TestResolver_FallbackOnConnRefused(t *testing.T) {
	srv, _ := startMock(t, fullExampleZones())
	closed := pickClosedPort(t)

	c := &Client{
		resolvers: []string{closed, srv.LocalAddr().String()},
		dns:       &dns.Client{Net: "tcp", Timeout: 1 * time.Second},
		limiter:   rate.NewLimiter(rateSustain, rateBurst),
		dialer:    &net.Dialer{},
	}

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rec.Hostname != "example.com" {
		t.Errorf("Hostname = %q, want example.com", rec.Hostname)
	}
}

// ---------------------------------------------------------------------
// Long fallback list: 4 closed + 1 live → succeeds
// ---------------------------------------------------------------------

func TestResolver_LongFallbackList(t *testing.T) {
	srv, _ := startMock(t, fullExampleZones())

	resolvers := []string{
		pickClosedPort(t),
		pickClosedPort(t),
		pickClosedPort(t),
		pickClosedPort(t),
		srv.LocalAddr().String(),
	}
	c := &Client{
		resolvers: resolvers,
		dns:       &dns.Client{Net: "tcp", Timeout: 1 * time.Second},
		limiter:   rate.NewLimiter(rateSustain, rateBurst),
		dialer:    &net.Dialer{},
	}

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(rec.NS) != 2 {
		t.Errorf("NS len = %d, want 2", len(rec.NS))
	}
}

// ---------------------------------------------------------------------
// Sticky picker: one preflight dial per Lookup
// ---------------------------------------------------------------------

func TestResolver_StickyPickerOneHandshake(t *testing.T) {
	srv, _ := startMock(t, fullExampleZones())

	var dials atomic.Int32
	d := &net.Dialer{
		ControlContext: func(_ context.Context, _, _ string, _ syscall.RawConn) error {
			dials.Add(1)
			return nil
		},
	}
	c := &Client{
		resolvers: []string{srv.LocalAddr().String()},
		dns:       &dns.Client{Net: "tcp", Timeout: 1 * time.Second},
		limiter:   rate.NewLimiter(rateSustain, rateBurst),
		dialer:    d,
	}

	if _, err := c.Lookup(context.Background(), "example.com"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("preflight dials = %d, want exactly 1", got)
	}
}

// ---------------------------------------------------------------------
// Lookup-level deadline
// ---------------------------------------------------------------------

func TestResolver_LookupDeadline(t *testing.T) {
	// Use a slow handler: serve via a custom dns.Server backed by a
	// handler that ties up the connection until the test's stop ch
	// closes (well past lookupDeadline). The Lookup MUST return on
	// its own internal 5s deadline, not after the upstream sleep ends.
	stop := make(chan struct{})
	srv := startSlowDNS(t, stop)
	// Cleanup order is LIFO: close(stop) MUST run BEFORE the dns.Server
	// Shutdown registered by startSlowDNS, so registered AFTER it here.
	t.Cleanup(func() { close(stop) })
	c := NewWithEndpoint(srv, &dns.Client{Net: "tcp", Timeout: 30 * time.Second})

	// Shorten lookupDeadline by wrapping ctx upstream. We test the
	// real const indirectly: total wall-clock < lookupDeadline + slack.
	start := time.Now()
	_, err := c.Lookup(context.Background(), "slow.example.")
	elapsed := time.Since(start)

	if elapsed > lookupDeadline+1*time.Second {
		t.Errorf("Lookup elapsed = %v, want <= %v", elapsed, lookupDeadline+1*time.Second)
	}
	// Slow handler never returns a useful response in time → NS goroutine
	// surfaces ctx-deadline-exceeded; Lookup propagates it. Per-type
	// goroutines may have raced, returning nil; deadline is the
	// dominant signal.
	if err == nil {
		t.Errorf("err = nil, want context-deadline-exceeded or ErrUnavailable")
	}
}

// startSlowDNS spins up a dns.Server on TCP that holds responses
// hostage until stop closes — the upstream never answers in time, so
// the resolver's own deadline must end the Lookup. Returns host:port.
func startSlowDNS(t *testing.T, stop <-chan struct{}) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &dns.Server{
		Listener: ln,
		Net:      "tcp",
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			<-stop
			r := new(dns.Msg)
			r.SetRcode(m, dns.RcodeServerFailure)
			_ = w.WriteMsg(r)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return ln.Addr().String()
}

// ---------------------------------------------------------------------
// Honors caller ctx deadline
// ---------------------------------------------------------------------

func TestResolver_HonorsCtxDeadline(t *testing.T) {
	_, c := startMock(t, fullExampleZones())

	// Pre-cancelled ctx → ctx.Err() must propagate.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("err = nil, want ctx error")
	}
	if errors.Is(err, ErrSelfThrottled) {
		t.Errorf("err = ErrSelfThrottled, want context.Canceled or DeadlineExceeded")
	}
}

// ---------------------------------------------------------------------
// Private A/AAAA stripped
// ---------------------------------------------------------------------

func TestResolver_PrivateAStripped(t *testing.T) {
	zones := fullExampleZones()
	zones["example.com."] = mockdns.Zone{
		A: []string{"10.0.0.1", "1.1.1.1"},
		// keep NS so existence-probe succeeds
		NS: []net.NS{{Host: "ns1.example.com."}},
	}
	_, c := startMock(t, zones)

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(rec.A) != 1 || rec.A[0].String() != "1.1.1.1" {
		t.Errorf("A = %v, want [1.1.1.1] (10.0.0.1 stripped)", rec.A)
	}
}

func TestResolver_PrivateAAAAStripped(t *testing.T) {
	zones := fullExampleZones()
	zones["example.com."] = mockdns.Zone{
		AAAA: []string{"fc00::1", "2606:4700::1111"},
		NS:   []net.NS{{Host: "ns1.example.com."}},
	}
	_, c := startMock(t, zones)

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(rec.AAAA) != 1 {
		t.Fatalf("AAAA len = %d, want 1; got %v", len(rec.AAAA), rec.AAAA)
	}
	if !rec.AAAA[0].Is6() || rec.AAAA[0].String() != "2606:4700::1111" {
		t.Errorf("AAAA = %v, want [2606:4700::1111]", rec.AAAA)
	}
	// Sanity: confirm fc00::/7 is treated as private by netip.
	if !netip.MustParseAddr("fc00::1").IsPrivate() {
		t.Error("netip says fc00::1 is not private; test premise broken")
	}
}

// ---------------------------------------------------------------------
// DNSSECVerified OR-aggregation across per-type responses
// ---------------------------------------------------------------------

func TestResolver_DNSSECADBit_OR(t *testing.T) {
	// AD bit set on the example.com zone → at least one per-type
	// response carries AD=true → Records.DNSSECVerified = true.
	_, c := startMock(t, fullExampleZones())

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !rec.DNSSECVerified {
		t.Errorf("DNSSECVerified = false, want true")
	}

	// And the negative: AD=false on every zone → false aggregate.
	zones := fullExampleZones()
	z := zones["example.com."]
	z.AD = false
	zones["example.com."] = z
	_, c2 := startMock(t, zones)

	rec, err = c2.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rec.DNSSECVerified {
		t.Errorf("DNSSECVerified = true, want false")
	}
}

// ---------------------------------------------------------------------
// Self-throttled: pre-burned limiter + cancelled deadline → ErrSelfThrottled
// ---------------------------------------------------------------------

func TestResolver_SelfThrottled(t *testing.T) {
	srv, _ := startMock(t, fullExampleZones())

	// Create a Client with an absurdly tight limiter (rate=0, burst=0)
	// so every Wait() blocks until ctx deadline → ErrSelfThrottled.
	c := &Client{
		resolvers: []string{srv.LocalAddr().String()},
		dns:       &dns.Client{Net: "tcp", Timeout: time.Second},
		limiter:   rate.NewLimiter(0, 0),
		dialer:    &net.Dialer{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Lookup(ctx, "example.com")
	if !errors.Is(err, ErrSelfThrottled) {
		t.Errorf("err = %v, want ErrSelfThrottled", err)
	}
}

// ---------------------------------------------------------------------
// Leading-underscore label (RFC 8552)
// ---------------------------------------------------------------------

func TestResolver_LeadingUnderscoreLabel(t *testing.T) {
	zones := map[string]mockdns.Zone{
		"_dmarc.example.com.": {
			TXT: []string{"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"},
			NS:  []net.NS{{Host: "ns1.example.com."}},
		},
	}
	_, c := startMock(t, zones)

	rec, err := c.Lookup(context.Background(), "_dmarc.example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !rec.TXT.HasDMARC {
		t.Errorf("TXT.HasDMARC = false, want true (got %+v)", rec.TXT)
	}
}

// ---------------------------------------------------------------------
// Per-type err must NOT cancel siblings
// ---------------------------------------------------------------------

func TestResolver_PerTypeErrDoesNotCancelSiblings(t *testing.T) {
	// Build a zone where TXT lookup will SERVFAIL but the rest
	// succeed. mockdns Zone.Err returns SERVFAIL on every type, so
	// instead we use a custom handler.
	srv := startSelectiveDNS(t)
	c := NewWithEndpoint(srv, &dns.Client{Net: "tcp", Timeout: 1 * time.Second})

	rec, err := c.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup: %v (TXT failure must not propagate)", err)
	}
	// NS populated → existence probe succeeded → other 8 types may
	// succeed independently. TXT is empty (the row drops), but A
	// remains populated.
	if len(rec.A) == 0 {
		t.Error("A = empty, want populated (TXT failure cancelled siblings)")
	}
	if rec.TXT.HasSPF || rec.TXT.HasDMARC || rec.TXT.OtherCount > 0 {
		t.Errorf("TXT unexpectedly populated: %+v", rec.TXT)
	}
}

// startSelectiveDNS spins up a TCP dns.Server that returns SERVFAIL
// for TXT queries and a small populated answer for everything else.
func startSelectiveDNS(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &dns.Server{
		Listener: ln,
		Net:      "tcp",
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			r := new(dns.Msg)
			r.SetReply(m)
			q := m.Question[0]
			switch q.Qtype {
			case dns.TypeTXT:
				r.Rcode = dns.RcodeServerFailure
			case dns.TypeA:
				r.Answer = append(r.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("93.184.216.34"),
				})
			case dns.TypeNS:
				r.Answer = append(r.Answer, &dns.NS{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 60},
					Ns:  "ns1.example.com.",
				})
			}
			_ = w.WriteMsg(r)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return ln.Addr().String()
}

// ---------------------------------------------------------------------
// Fixture replay: parse stored .bin dumps via lookupX-equivalent path
// ---------------------------------------------------------------------

func TestResolver_FixtureReplay(t *testing.T) {
	cases := []struct {
		file  string
		qtype uint16
		check func(t *testing.T, resp *dns.Msg)
	}{
		{"a.bin", dns.TypeA, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in a.bin")
			}
		}},
		{"aaaa.bin", dns.TypeAAAA, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in aaaa.bin")
			}
		}},
		{"cname.bin", dns.TypeCNAME, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in cname.bin")
			}
		}},
		{"mx.bin", dns.TypeMX, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in mx.bin")
			}
		}},
		{"ns.bin", dns.TypeNS, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in ns.bin")
			}
		}},
		{"txt.bin", dns.TypeTXT, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in txt.bin")
			}
		}},
		{"soa.bin", dns.TypeSOA, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in soa.bin")
			}
		}},
		{"caa.bin", dns.TypeCAA, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in caa.bin")
			}
		}},
		{"srv.bin", dns.TypeSRV, func(t *testing.T, resp *dns.Msg) {
			if len(resp.Answer) == 0 {
				t.Error("no answers in srv.bin")
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "testdata", tc.file)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			m := new(dns.Msg)
			if err := m.Unpack(raw); err != nil {
				t.Fatalf("unpack %s: %v", path, err)
			}
			if m.Question[0].Qtype != tc.qtype {
				t.Errorf("qtype = %d, want %d", m.Question[0].Qtype, tc.qtype)
			}
			tc.check(t, m)
		})
	}
}

// ---------------------------------------------------------------------
// LiveDoT: real 1.1.1.1 (skipped under -short)
// ---------------------------------------------------------------------

func TestResolver_LiveDoT(t *testing.T) {
	if testing.Short() {
		t.Skip("LiveDoT skipped under -short")
	}
	c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec, err := c.Lookup(ctx, "cloudflare.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(rec.A) == 0 {
		t.Error("A = empty for cloudflare.com")
	}
}
