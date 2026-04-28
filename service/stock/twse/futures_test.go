package twse_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

// taifexResponse describes one canned reply for a (commodity, date)
// pair. CSV body is in UTF-8 here for test ergonomics; the fixture
// server transcodes to BIG5 on the wire to match production behavior.
type taifexResponse struct {
	csvUTF8 string
	noData  bool // when true, server returns the "查無資料" HTML page
}

func newTAIFEXServer(t *testing.T, responses map[string]taifexResponse) *httptest.Server {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = r.ParseForm()
		commodity := r.FormValue("commodityId")
		date := r.FormValue("queryStartDate")
		key := commodity + "@" + date

		resp, ok := responses[key]
		w.Header().Set("Content-Type", "text/html;charset=UTF-8")
		switch {
		case !ok || resp.noData:
			_, _ = w.Write([]byte(`<html><body><script>alert("查無資料");</script></body></html>`))
			return
		default:
			big5, _, err := transform.Bytes(traditionalchinese.Big5.NewEncoder(), []byte(resp.csvUTF8))
			if err != nil {
				t.Fatalf("encode big5: %v", err)
			}
			_, _ = w.Write(big5)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// taifexCSV builds a fixture CSV with header + 3 institutional rows
// for one commodity. dealerNet / trustNet / foreignNet are the per-row
// 多空未平倉口數淨額 values; long_oi / short_oi columns are zeroed
// since the parser only reads the net column.
func taifexCSV(date, commodity string, dealerNet, trustNet, foreignNet int64) string {
	header := "日期,商品名稱,身份別,多方交易口數,多方交易契約金額(千元),空方交易口數,空方交易契約金額(千元),多空交易口數淨額,多空交易契約金額淨額(千元),多方未平倉口數,多方未平倉契約金額(千元),空方未平倉口數,空方未平倉契約金額(千元),多空未平倉口數淨額,多空未平倉契約金額淨額(千元)"
	row := func(party string, net int64) string {
		return strings.Join([]string{
			date, commodity, party,
			"0", "0", "0", "0", "0", "0",
			"0", "0", "0", "0",
			formatInt(net),
			"0",
		}, ",")
	}
	return strings.Join([]string{
		header,
		row("自營商", dealerNet),
		row("投信", trustNet),
		row("外資及陸資", foreignNet),
	}, "\r\n")
}

func formatInt(v int64) string {
	if v < 0 {
		return "-" + formatInt(-v)
	}
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	out := make([]byte, 0, 8)
	for v > 0 {
		out = append([]byte{digits[v%10]}, out...)
		v /= 10
	}
	return string(out)
}

func TestGetRetailFuturesBothContracts(t *testing.T) {
	// MXF on 2026/04/24: dealer -2594, trust +79, foreign +3667 → inst +1152 → retail -1152.
	// TMF on 2026/04/24: dealer +17586, trust 0, foreign +4060 → inst +21646 → retail -21646.
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", -2594, 79, 3667)},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 17586, 0, 4060)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -1152 {
		t.Errorf("MXFNet = %d, want -1152", got.MXFNet)
	}
	if got.TMFNet != -21646 {
		t.Errorf("TMFNet = %d, want -21646", got.TMFNet)
	}
}

func TestGetRetailFuturesLooksBackOnNoData(t *testing.T) {
	// Asking for 2026/04/26 (Sun) — no data. Walk back to Friday 2026/04/24.
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/26": {noData: true},
		"MXF@2026/04/25": {noData: true},
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", 100, 0, 200)},
		"TMF@2026/04/26": {noData: true},
		"TMF@2026/04/25": {noData: true},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 50, 0, 50)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-26")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -300 {
		t.Errorf("MXFNet = %d, want -300", got.MXFNet)
	}
	if got.TMFNet != -100 {
		t.Errorf("TMFNet = %d, want -100", got.TMFNet)
	}
}

func TestGetRetailFuturesDisabledWhenEndpointUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.GetRetailFutures(context.Background(), time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestGetRetailFuturesExhaustedLookbackReturnsErrUnavailable(t *testing.T) {
	// Every probe returns no-data — full lookback exhausts.
	srv := newTAIFEXServer(t, nil)
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	_, err := p.GetRetailFutures(context.Background(), time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestCachedGetRetailFuturesPassthrough(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", -100, 0, 0)},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 0, 0, 200)},
	})
	inner := twse.NewWithEndpoints("", "", "", srv.Client())
	inner.SetTAIFEXFuturesEndpoint(srv.URL)
	cached := twse.NewCached(inner)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := cached.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("Cached.GetRetailFutures: %v", err)
	}
	if got.MXFNet != 100 {
		t.Errorf("MXFNet = %d, want 100", got.MXFNet)
	}
	if got.TMFNet != -200 {
		t.Errorf("TMFNet = %d, want -200", got.TMFNet)
	}
}
