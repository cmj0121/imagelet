//go:build ignore

// gen_fixtures.go captures one dns.Msg.Pack() wire-format dump per
// record type from a live 1.1.1.1 query and stores it under
// service/dns/testdata/<type>.bin (R17). Run once at implementation
// time:
//
//	go run service/dns/testdata/gen_fixtures.go
//
// Re-run when 1.1.1.1's response shape changes meaningfully. The
// fixtures cover "did 1.1.1.1 change its truncation behavior" cheaply
// in offline CI; mockdns covers the per-type parsing logic.
//
// Each fixture is a raw wire-format DNS response (suitable for
// dns.Msg.Unpack). The query domain is chosen to maximize the chance
// of every record type populating:
//
//	A      → cloudflare.com
//	AAAA   → cloudflare.com
//	CNAME  → www.github.com
//	MX     → google.com
//	NS     → cloudflare.com
//	TXT    → cloudflare.com   (SPF + verifications)
//	SOA    → cloudflare.com
//	CAA    → letsencrypt.org
//	SRV    → _xmpp-client._tcp.jabber.org
package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/miekg/dns"
)

type fixture struct {
	host  string
	qtype uint16
	name  string
}

func main() {
	cases := []fixture{
		{"cloudflare.com.", dns.TypeA, "a"},
		{"cloudflare.com.", dns.TypeAAAA, "aaaa"},
		{"www.github.com.", dns.TypeCNAME, "cname"},
		{"google.com.", dns.TypeMX, "mx"},
		{"cloudflare.com.", dns.TypeNS, "ns"},
		{"cloudflare.com.", dns.TypeTXT, "txt"},
		{"cloudflare.com.", dns.TypeSOA, "soa"},
		{"letsencrypt.org.", dns.TypeCAA, "caa"},
		{"_xmpp-client._tcp.jabber.org.", dns.TypeSRV, "srv"},
	}

	c := &dns.Client{
		Net:     "tcp-tls",
		Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			ServerName: "cloudflare-dns.com",
			MinVersion: tls.VersionTLS12,
		},
	}

	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)

	for _, f := range cases {
		m := new(dns.Msg)
		m.SetQuestion(f.host, f.qtype)
		m.SetEdns0(4096, false)
		m.AuthenticatedData = true

		resp, _, err := c.Exchange(m, "1.1.1.1:853")
		if err != nil {
			log.Fatalf("exchange %s: %v", f.name, err)
		}
		raw, err := resp.Pack()
		if err != nil {
			log.Fatalf("pack %s: %v", f.name, err)
		}
		out := filepath.Join(dir, f.name+".bin")
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			log.Fatalf("write %s: %v", out, err)
		}
		fmt.Printf("%-6s rcode=%d answers=%d ad=%t bytes=%d → %s\n",
			f.name, resp.Rcode, len(resp.Answer), resp.AuthenticatedData, len(raw), out)
	}
}
