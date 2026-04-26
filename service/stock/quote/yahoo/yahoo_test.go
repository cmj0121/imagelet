package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// fixtureServer returns an httptest.Server that serves the named fixture
// from testdata/ for any path. Status overrides the response code.
func fixtureServer(t *testing.T, name string, status int) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	}))
}

func newProvider(server *httptest.Server) *Provider {
	return NewWithEndpoint(server.URL+"/%s", server.Client())
}

func TestGetOpenMarket(t *testing.T) {
	srv := fixtureServer(t, "gspc_open.json", 0)
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if q.Symbol != "^GSPC" {
		t.Errorf("symbol = %q, want %q", q.Symbol, "^GSPC")
	}
	if q.Last == 0 {
		t.Errorf("Last == 0, want non-zero")
	}
	if q.PrevClose == 0 {
		t.Errorf("PrevClose == 0, want non-zero")
	}
	if q.Currency == "" {
		t.Errorf("Currency is empty")
	}
	if q.AsOf.IsZero() {
		t.Errorf("AsOf is zero")
	}
}

func TestGetClosedMarket(t *testing.T) {
	srv := fixtureServer(t, "gspc_closed.json", 0)
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !q.IsClosed {
		t.Errorf("IsClosed = false, want true (fixture's regular trading window is in 2023)")
	}
}

func TestGetEmptyResultIsErrUnavailable(t *testing.T) {
	srv := fixtureServer(t, "not_found.json", 0)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^NOPE")
	if !errors.Is(err, quote.ErrUnavailable) {
		t.Errorf("err = %v, want quote.ErrUnavailable", err)
	}
}

func TestGetUpstream500ReturnsError(t *testing.T) {
	srv := fixtureServer(t, "not_found.json", http.StatusInternalServerError)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err == nil {
		t.Fatalf("err is nil; want non-nil for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err %q does not mention 500 status", err)
	}
}

func TestGetYahooErrorJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"chart":{"result":[],"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^NOPE")
	if err == nil {
		t.Fatalf("err is nil; want non-nil for Yahoo error JSON")
	}
	if !strings.Contains(err.Error(), "delisted") {
		t.Errorf("err %q does not include the upstream description", err)
	}
}

// TestMarketClosed exercises the marketClosed helper directly with synthetic
// timestamps so we don't need to mock time.Now() in the Get path.
func TestMarketClosed(t *testing.T) {
	mkMeta := func(start, end int64) chartMeta {
		var m chartMeta
		m.CurrentTradingPeriod.Regular.Start = start
		m.CurrentTradingPeriod.Regular.End = end
		return m
	}

	cases := []struct {
		name  string
		now   int64
		start int64
		end   int64
		want  bool
	}{
		{"before_open", 100, 200, 300, true},
		{"at_open", 200, 200, 300, false},
		{"middle_of_session", 250, 200, 300, false},
		{"at_close", 300, 200, 300, true}, // .end is exclusive
		{"after_close", 400, 200, 300, true},
		{"end_in_past_weekend", 1700100000, 1700000000, 1700020000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marketClosed(mkMeta(tc.start, tc.end), tc.now)
			if got != tc.want {
				t.Errorf("marketClosed(start=%d, end=%d, now=%d) = %v, want %v",
					tc.start, tc.end, tc.now, got, tc.want)
			}
		})
	}
}
