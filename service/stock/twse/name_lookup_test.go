package twse_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

// nameLookupServer is a minimal MIS getStockInfo fixture for LookupName.
// Each canned row carries a Chinese name keyed by stockID; the prefix
// (tse_ / otc_) selects which map answers, mirroring production MIS.
type nameLookupServer struct {
	srv      *httptest.Server
	misCalls int64 // atomic
}

func newNameLookupServer(t *testing.T, tseNames, otcNames map[string]string) *nameLookupServer {
	t.Helper()
	lbs := &nameLookupServer{}
	lbs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/getStockInfo.jsp") {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt64(&lbs.misCalls, 1)
		ex := r.URL.Query().Get("ex_ch")
		parts := strings.Split(ex, "|")
		msg := make([]map[string]string, 0, len(parts))
		for _, p := range parts {
			var (
				code  string
				names map[string]string
			)
			switch {
			case strings.HasPrefix(p, "tse_"):
				code = strings.TrimSuffix(strings.TrimPrefix(p, "tse_"), ".tw")
				names = tseNames
			case strings.HasPrefix(p, "otc_"):
				code = strings.TrimSuffix(strings.TrimPrefix(p, "otc_"), ".tw")
				names = otcNames
			default:
				continue
			}
			n, ok := names[code]
			if !ok {
				continue
			}
			msg = append(msg, map[string]string{"c": code, "n": n})
		}
		body := map[string]any{
			"rtcode":   "0000",
			"msgArray": msg,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(lbs.srv.Close)
	return lbs
}

func newProviderWithName(srv *nameLookupServer) *twse.HTTPProvider {
	p := twse.NewWithEndpoints("", "", "", srv.srv.Client())
	p.SetLiveBreadthEndpoints("", "", srv.srv.URL+"/getStockInfo.jsp")
	return p
}

func TestLookupNameTSE(t *testing.T) {
	srv := newNameLookupServer(t,
		map[string]string{"2330": "台積電"},
		nil,
	)
	p := newProviderWithName(srv)

	got, err := p.LookupName(context.Background(), "2330", false)
	if err != nil {
		t.Fatalf("LookupName: %v", err)
	}
	if got != "台積電" {
		t.Errorf("got %q, want %q", got, "台積電")
	}
}

func TestLookupNameOTC(t *testing.T) {
	srv := newNameLookupServer(t,
		nil,
		map[string]string{"5274": "信驊"},
	)
	p := newProviderWithName(srv)

	got, err := p.LookupName(context.Background(), "5274", true)
	if err != nil {
		t.Fatalf("LookupName: %v", err)
	}
	if got != "信驊" {
		t.Errorf("got %q, want %q", got, "信驊")
	}
}

func TestLookupNameMissingReturnsErrUnavailable(t *testing.T) {
	srv := newNameLookupServer(t, nil, nil)
	p := newProviderWithName(srv)

	_, err := p.LookupName(context.Background(), "9999", false)
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestLookupNameMemoizes(t *testing.T) {
	srv := newNameLookupServer(t,
		map[string]string{"2330": "台積電"},
		nil,
	)
	p := newProviderWithName(srv)

	for i := 0; i < 5; i++ {
		_, err := p.LookupName(context.Background(), "2330", false)
		if err != nil {
			t.Fatalf("LookupName iter %d: %v", i, err)
		}
	}
	if calls := atomic.LoadInt64(&srv.misCalls); calls != 1 {
		t.Errorf("MIS calls = %d, want 1 (forever-cache after first hit)", calls)
	}
}

func TestLookupNameDisabledWhenEndpointUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.LookupName(context.Background(), "2330", false)
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable when MIS endpoint unset", err)
	}
}

func TestCachedLookupNamePassthrough(t *testing.T) {
	srv := newNameLookupServer(t,
		map[string]string{"2330": "台積電"},
		nil,
	)
	inner := newProviderWithName(srv)
	cached := twse.NewCached(inner)

	got, err := cached.LookupName(context.Background(), "2330", false)
	if err != nil {
		t.Fatalf("LookupName: %v", err)
	}
	if got != "台積電" {
		t.Errorf("got %q, want %q", got, "台積電")
	}
}
