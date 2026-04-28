package twse_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

// twt93uServer is a minimal TWT93U fixture: returns a JSON envelope
// with one row per known stock id keyed by ?date=. Missing dates yield
// stat="" (not "OK") which the parser treats as ErrUnavailable.
func newTWT93UServer(t *testing.T, byDate map[string][][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		rows, ok := byDate[date]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_, _ = w.Write([]byte(`{"stat":"","date":"","data":[]}`))
			return
		}
		out := `{"stat":"OK","date":"` + date + `","data":[`
		for i, r := range rows {
			if i > 0 {
				out += ","
			}
			out += `[`
			for j, c := range r {
				if j > 0 {
					out += ","
				}
				out += `"` + c + `"`
			}
			out += `]`
		}
		out += `]}`
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// twt93uRow builds a 15-column row matching TWSE's TWT93U layout. Only
// stockID, name, and 借券當日餘額 (col 12) are meaningful for the
// parser; the rest are zeros that exercise the column-count check.
func twt93uRow(stockID, name string, lendingBalance int64) []string {
	row := make([]string, 15)
	row[0] = stockID
	row[1] = name
	row[12] = fmt.Sprintf("%d", lendingBalance)
	for i := range row {
		if row[i] == "" {
			row[i] = "0"
		}
	}
	return row
}

func TestGetSecuritiesLendingReturnsRow(t *testing.T) {
	srv := newTWT93UServer(t, map[string][][]string{
		"20260424": {
			twt93uRow("2330", "台積電", 4562680),
			twt93uRow("2317", "鴻海", 1234567),
		},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTWT93UEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetSecuritiesLending(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("GetSecuritiesLending: %v", err)
	}
	if got.Balance != 4562680 {
		t.Errorf("Balance = %d, want 4562680", got.Balance)
	}
	if got.Name != "台積電" {
		t.Errorf("Name = %q, want 台積電", got.Name)
	}
	if !got.Has() {
		t.Error("Has() should be true for 4.5M-share balance")
	}
}

func TestGetSecuritiesLendingMissingReturnsErrUnavailable(t *testing.T) {
	srv := newTWT93UServer(t, map[string][][]string{
		"20260424": {twt93uRow("2330", "台積電", 100)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTWT93UEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	_, err := p.GetSecuritiesLending(context.Background(), "9999", asOf)
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestGetSecuritiesLendingDisabledWhenEndpointUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.GetSecuritiesLending(context.Background(), "2330", time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

func TestCachedGetSecuritiesLendingPassthrough(t *testing.T) {
	srv := newTWT93UServer(t, map[string][][]string{
		"20260424": {twt93uRow("2330", "台積電", 555000)},
	})
	inner := twse.NewWithEndpoints("", "", "", srv.Client())
	inner.SetTWT93UEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")
	cached := twse.NewCached(inner)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := cached.GetSecuritiesLending(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("Cached.GetSecuritiesLending: %v", err)
	}
	if got.Balance != 555000 {
		t.Errorf("Balance = %d, want 555000", got.Balance)
	}
}
