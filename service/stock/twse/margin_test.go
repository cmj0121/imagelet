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

// miMargnStockServer is a minimal MI_MARGN fixture: returns a JSON
// envelope with one row per known stock id keyed by ?date=. The real
// upstream nests data under a `tables` array (one summary table, one
// per-stock 融資融券彙總 grid) and the parser walks every table; the
// fixture emits a single per-stock table to keep the body small.
// Missing dates yield stat="" (not "OK") which the parser treats as
// ErrUnavailable.
func newMIMARGNStockServer(t *testing.T, byDate map[string][][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		rows, ok := byDate[date]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_, _ = w.Write([]byte(`{"stat":"","date":"","tables":[]}`))
			return
		}
		out := `{"stat":"OK","date":"` + date + `","tables":[{"title":"融資融券彙總","fields":[],"data":[`
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
		out += `]}]}`
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// miMargnRow builds a 16-column MI_MARGN row matching TWSE's layout.
// Only stockID, name, 融資今日餘額 (col 6), and 融券今日餘額 (col 12)
// are meaningful for the parser; the rest are zeros that exercise
// the column-count check.
func miMargnRow(stockID, name string, marginBalance, shortBalance int64) []string {
	row := make([]string, 16)
	row[0] = stockID
	row[1] = name
	row[6] = fmt.Sprintf("%d", marginBalance)
	row[12] = fmt.Sprintf("%d", shortBalance)
	for i := range row {
		if row[i] == "" {
			row[i] = "0"
		}
	}
	return row
}

func TestGetStockMarginReturnsRow(t *testing.T) {
	srv := newMIMARGNStockServer(t, map[string][][]string{
		"20260424": {
			miMargnRow("2330", "台積電", 12345678, 234567),
			miMargnRow("2317", "鴻海", 9876543, 12345),
		},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetMIMARGNStockEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetStockMargin(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("GetStockMargin: %v", err)
	}
	if got.MarginBalance != 12345678 {
		t.Errorf("MarginBalance = %d, want 12345678", got.MarginBalance)
	}
	if got.ShortBalance != 234567 {
		t.Errorf("ShortBalance = %d, want 234567", got.ShortBalance)
	}
	if got.Name != "台積電" {
		t.Errorf("Name = %q, want 台積電", got.Name)
	}
	if !got.Has() {
		t.Error("Has() should be true when balances are populated")
	}
}

func TestGetStockMarginMissingReturnsErrUnavailable(t *testing.T) {
	srv := newMIMARGNStockServer(t, map[string][][]string{
		"20260424": {miMargnRow("2330", "台積電", 100, 50)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetMIMARGNStockEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	_, err := p.GetStockMargin(context.Background(), "9999", asOf)
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestGetStockMarginDisabledWhenEndpointUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.GetStockMargin(context.Background(), "2330", time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

func TestCachedGetStockMarginPassthrough(t *testing.T) {
	srv := newMIMARGNStockServer(t, map[string][][]string{
		"20260424": {miMargnRow("2330", "台積電", 5000000, 100000)},
	})
	inner := twse.NewWithEndpoints("", "", "", srv.Client())
	inner.SetMIMARGNStockEndpoint(srv.URL + "?date=%s&selectType=ALL&response=json")
	cached := twse.NewCached(inner)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := cached.GetStockMargin(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("Cached.GetStockMargin: %v", err)
	}
	if got.MarginBalance != 5000000 {
		t.Errorf("MarginBalance = %d, want 5000000", got.MarginBalance)
	}
	if got.ShortBalance != 100000 {
		t.Errorf("ShortBalance = %d, want 100000", got.ShortBalance)
	}
}
