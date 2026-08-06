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
	status  int  // when non-zero, server replies with this status and no body
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
		case ok && resp.status != 0:
			w.WriteHeader(resp.status)
			return
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

// taifexPartyRow is one 身份別 row for taifexCSVRows.
type taifexPartyRow struct {
	party string
	net   int64
}

// taifexCSVRows builds a fixture CSV with header + the given 身份別
// rows in the given order — lets tests exercise party-name variants
// and row-order shuffles. long_oi / short_oi columns are zeroed since
// the parser only reads the 身份別 and net columns.
func taifexCSVRows(date, commodity string, rows ...taifexPartyRow) string {
	header := "日期,商品名稱,身份別,多方交易口數,多方交易契約金額(千元),空方交易口數,空方交易契約金額(千元),多空交易口數淨額,多空交易契約金額淨額(千元),多方未平倉口數,多方未平倉契約金額(千元),空方未平倉口數,空方未平倉契約金額(千元),多空未平倉口數淨額,多空未平倉契約金額淨額(千元)"
	lines := []string{header}
	for _, r := range rows {
		lines = append(lines, strings.Join([]string{
			date, commodity, r.party,
			"0", "0", "0", "0", "0", "0",
			"0", "0", "0", "0",
			formatInt(r.net),
			"0",
		}, ","))
	}
	return strings.Join(lines, "\r\n")
}

// taifexCSV builds the canonical 3-row fixture (自營商 / 投信 /
// 外資及陸資 in the upstream's usual order) for one commodity.
// dealerNet / trustNet / foreignNet are the per-row 多空未平倉口數淨額
// values.
func taifexCSV(date, commodity string, dealerNet, trustNet, foreignNet int64) string {
	return taifexCSVRows(date, commodity,
		taifexPartyRow{"自營商", dealerNet},
		taifexPartyRow{"投信", trustNet},
		taifexPartyRow{"外資及陸資", foreignNet},
	)
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

// TestGetRetailFuturesTXFBreakdown pins the per-身份別 attribution for
// the TXF (大台) contract alongside the unchanged retail derivation
// for the minis.
func TestGetRetailFuturesTXFBreakdown(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", -2594, 79, 3667)},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 17586, 0, 4060)},
		"TXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "臺股期貨", -12083, 2214, 35916)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.TXFDealerNet != -12083 {
		t.Errorf("TXFDealerNet = %d, want -12083", got.TXFDealerNet)
	}
	if got.TXFTrustNet != 2214 {
		t.Errorf("TXFTrustNet = %d, want 2214", got.TXFTrustNet)
	}
	if got.TXFForeignNet != 35916 {
		t.Errorf("TXFForeignNet = %d, want 35916", got.TXFForeignNet)
	}
	if got.TXFInstNet != 26047 {
		t.Errorf("TXFInstNet = %d, want 26047 (dealer+trust+foreign)", got.TXFInstNet)
	}
	if !got.HasTXF() {
		t.Errorf("HasTXF() = false, want true")
	}
	// Retail derivation must be untouched by the TXF addition.
	if got.MXFNet != -1152 {
		t.Errorf("MXFNet = %d, want -1152", got.MXFNet)
	}
	if got.TMFNet != -21646 {
		t.Errorf("TMFNet = %d, want -21646", got.TMFNet)
	}
}

// TestGetRetailFuturesPartyNameTolerance pins the one alias the table
// carries: the pre-rename bare 外資 must attribute identically to the
// canonical 外資及陸資.
func TestGetRetailFuturesPartyNameTolerance(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSVRows("2026/04/24", "小型臺指期貨",
			taifexPartyRow{"自營商", -2594},
			taifexPartyRow{"投信", 79},
			taifexPartyRow{"外資", 3667},
		)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -1152 {
		t.Errorf("MXFNet = %d, want -1152 (外資 must attribute like 外資及陸資)", got.MXFNet)
	}
}

// TestGetRetailFuturesSuffixedPartyFailsLoudly pins the exact-match
// rule. futContractsDateDown emits three unsuffixed rows; suffixed
// parties belong to the options file. A suffixed row here means the
// layout changed, and under a prefix rule a subtotal alongside
// sub-rows would silently double-count — so it must error.
func TestGetRetailFuturesSuffixedPartyFailsLoudly(t *testing.T) {
	bad := taifexCSVRows("2026/04/24", "小型臺指期貨",
		taifexPartyRow{"自營商(避險)", -2594},
		taifexPartyRow{"投信", 79},
		taifexPartyRow{"外資及陸資", 3667},
	)
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: bad},
		"TMF@2026/04/24": {csvUTF8: bad},
		"TXF@2026/04/24": {csvUTF8: bad},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	if _, err := p.GetRetailFutures(context.Background(), asOf); err == nil {
		t.Fatal("GetRetailFutures: want error for suffixed 身份別, got nil")
	}
}

// TestGetRetailFuturesDuplicatePartyFailsLoudly pins the dedupe guard:
// a repeated party (including the two foreign spellings in one file)
// would inflate the breakdown, so it errors rather than accumulating.
func TestGetRetailFuturesDuplicatePartyFailsLoudly(t *testing.T) {
	bad := taifexCSVRows("2026/04/24", "小型臺指期貨",
		taifexPartyRow{"自營商", -2594},
		taifexPartyRow{"投信", 79},
		taifexPartyRow{"外資", 3667},
		taifexPartyRow{"外資及陸資", 3667},
	)
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: bad},
		"TMF@2026/04/24": {csvUTF8: bad},
		"TXF@2026/04/24": {csvUTF8: bad},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	_, err := p.GetRetailFutures(context.Background(), asOf)
	if err == nil {
		t.Fatal("GetRetailFutures: want error for duplicate 身份別, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err = %v, want a duplicate-party error", err)
	}
}

// TestGetRetailFuturesBlankRowsSkipped pins that all-empty padding
// lines (a trailing ",,,," the upstream sometimes appends) are skipped
// rather than hitting the fail-loud default as `unknown 身份別 ""`.
func TestGetRetailFuturesBlankRowsSkipped(t *testing.T) {
	withPadding := taifexCSV("2026/04/24", "小型臺指期貨", -2594, 79, 3667) +
		"\r\n" + strings.Repeat(",", 14)
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: withPadding},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -1152 {
		t.Errorf("MXFNet = %d, want -1152 (padding row must be skipped)", got.MXFNet)
	}
}

// TestGetRetailFuturesShortRowFailsLoudly pins the short-row posture:
// a non-blank row missing the pinned columns is a layout change and
// errors, the same as an unattributable party — silently dropping it
// would corrupt the retail derivation identically.
func TestGetRetailFuturesShortRowFailsLoudly(t *testing.T) {
	bad := taifexCSV("2026/04/24", "小型臺指期貨", -2594, 79, 3667) +
		"\r\n2026/04/24,小型臺指期貨,自營商,0"
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: bad},
		"TMF@2026/04/24": {csvUTF8: bad},
		"TXF@2026/04/24": {csvUTF8: bad},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	_, err := p.GetRetailFutures(context.Background(), asOf)
	if err == nil {
		t.Fatal("GetRetailFutures: want error for short row, got nil")
	}
	if !strings.Contains(err.Error(), "columns") {
		t.Errorf("err = %v, want a column-count error", err)
	}
}

// TestGetRetailFuturesRowsOutOfOrder pins attribution by 身份別 value
// rather than row position.
func TestGetRetailFuturesRowsOutOfOrder(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"TXF@2026/04/24": {csvUTF8: taifexCSVRows("2026/04/24", "臺股期貨",
			taifexPartyRow{"外資及陸資", 35916},
			taifexPartyRow{"自營商", -12083},
			taifexPartyRow{"投信", 2214},
		)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.TXFDealerNet != -12083 || got.TXFTrustNet != 2214 || got.TXFForeignNet != 35916 {
		t.Errorf("breakdown = (dealer=%d, trust=%d, foreign=%d), want (-12083, 2214, 35916) regardless of row order",
			got.TXFDealerNet, got.TXFTrustNet, got.TXFForeignNet)
	}
}

// TestGetRetailFuturesUnknownPartyFailsLoudly pins the fail-loud rule:
// a row with an unrecognized 身份別 must error rather than be silently
// dropped — a silent drop would corrupt the retail derivation. All
// three commodities serve the bad CSV so the per-contract best-effort
// posture can't mask the failure.
func TestGetRetailFuturesUnknownPartyFailsLoudly(t *testing.T) {
	bad := taifexCSVRows("2026/04/24", "小型臺指期貨",
		taifexPartyRow{"自營商", 1},
		taifexPartyRow{"散戶", 2},
	)
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: bad},
		"TMF@2026/04/24": {csvUTF8: bad},
		"TXF@2026/04/24": {csvUTF8: bad},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	_, err := p.GetRetailFutures(context.Background(), asOf)
	if err == nil {
		t.Fatal("GetRetailFutures: want error for unknown 身份別, got nil")
	}
	if !strings.Contains(err.Error(), "身份別") {
		t.Errorf("err = %v, want mention of the unknown 身份別", err)
	}
}

// TestGetRetailFuturesTXFNoDataStillReturnsMinis pins the best-effort
// posture: the TXF walk-back exhausting (no data on any probed day)
// must zero only the TXF fields and still surface the MXF/TMF answer.
func TestGetRetailFuturesTXFNoDataStillReturnsMinis(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", 100, 0, 200)},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 50, 0, 50)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -300 || got.TMFNet != -100 {
		t.Errorf("minis = (%d, %d), want (-300, -100)", got.MXFNet, got.TMFNet)
	}
	if got.HasTXF() {
		t.Errorf("HasTXF() = true, want false when TXF had no data")
	}
	if got.TXFDealerNet != 0 || got.TXFTrustNet != 0 || got.TXFForeignNet != 0 || got.TXFInstNet != 0 {
		t.Errorf("TXF fields = (%d, %d, %d, %d), want all zero",
			got.TXFDealerNet, got.TXFTrustNet, got.TXFForeignNet, got.TXFInstNet)
	}
}

// TestGetRetailFuturesTXFOnlyKeepsHasAnyFalse pins the reverse
// best-effort direction plus the HasAny gating: a TXF-only day must
// return the breakdown without error, but HasAny (which gates the 散戶
// render group) must stay false so no label-only group renders.
func TestGetRetailFuturesTXFOnlyKeepsHasAnyFalse(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		"TXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "臺股期貨", 100, -30, 500)},
	})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if !got.HasTXF() {
		t.Errorf("HasTXF() = false, want true")
	}
	if got.HasAny() {
		t.Errorf("HasAny() = true, want false (TXF data must not light up the 散戶 group)")
	}
	if got.MXFNet != 0 || got.TMFNet != 0 {
		t.Errorf("minis = (%d, %d), want (0, 0)", got.MXFNet, got.TMFNet)
	}
	if wantAsOf, _ := time.Parse("2006-01-02", "2026-04-24"); !got.AsOf.Equal(wantAsOf) {
		t.Errorf("AsOf = %v, want %v (TXF date must fill in when the minis are empty)", got.AsOf, wantAsOf)
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

// --- FetchTAIFEXFuturesExact: the path production actually runs ---
//
// CachedRetailFutures drives this method, not GetRetailFutures, so the
// field mapping and the per-commodity failure posture are pinned here
// directly rather than only through the uncached twin.

// exactTestProvider builds a provider wired to a fixture server for
// the given canned responses.
func exactTestProvider(t *testing.T, responses map[string]taifexResponse) *twse.HTTPProvider {
	t.Helper()
	srv := newTAIFEXServer(t, responses)
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXFuturesEndpoint(srv.URL)
	return p
}

// TestFetchTAIFEXFuturesExactMapsFields pins the field mapping on the
// production path: retail derivation for the minis, raw per-身份別
// values for TXF, and the requested day echoed as AsOf.
func TestFetchTAIFEXFuturesExactMapsFields(t *testing.T) {
	p := exactTestProvider(t, map[string]taifexResponse{
		"MXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "小型臺指期貨", -2594, 79, 3667)},
		"TMF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "微型臺指期貨", 17586, 0, 4060)},
		"TXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "臺股期貨", -12083, 2214, 35916)},
	})

	day := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	got, found, err := p.FetchTAIFEXFuturesExact(context.Background(), day)
	if err != nil {
		t.Fatalf("FetchTAIFEXFuturesExact: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if got.MXFNet != -1152 || got.TMFNet != -21646 {
		t.Errorf("minis = (%d, %d), want (-1152, -21646)", got.MXFNet, got.TMFNet)
	}
	if got.TXFDealerNet != -12083 || got.TXFTrustNet != 2214 || got.TXFForeignNet != 35916 || got.TXFInstNet != 26047 {
		t.Errorf("TXF = (dealer=%d, trust=%d, foreign=%d, inst=%d), want (-12083, 2214, 35916, 26047)",
			got.TXFDealerNet, got.TXFTrustNet, got.TXFForeignNet, got.TXFInstNet)
	}
	if !got.AsOf.Equal(day) {
		t.Errorf("AsOf = %v, want %v", got.AsOf, day)
	}
}

// TestFetchTAIFEXFuturesExactPartialFailure pins the containment rule
// on the production path: the fetch fails only when ALL THREE
// contracts fail. Every one- and two-contract failure combination must
// still deliver whatever survived — most importantly a TXF failure
// must not take down the rendered 散戶 MXF/TMF group.
func TestFetchTAIFEXFuturesExactPartialFailure(t *testing.T) {
	const day = "2026/04/24"
	ok := map[string]taifexResponse{
		"MXF@" + day: {csvUTF8: taifexCSV(day, "小型臺指期貨", 100, 0, 200)},
		"TMF@" + day: {csvUTF8: taifexCSV(day, "微型臺指期貨", 50, 0, 50)},
		"TXF@" + day: {csvUTF8: taifexCSV(day, "臺股期貨", -12083, 2214, 35916)},
	}
	boom := taifexResponse{status: http.StatusInternalServerError}

	tests := []struct {
		name        string
		fail        []string // commodities whose POST returns 500
		wantErr     bool
		wantFound   bool
		wantMXF     int64
		wantTMF     int64
		wantTXF     bool // HasTXF()
		wantPartial bool
	}{
		{
			name: "all published", wantFound: true,
			wantMXF: -300, wantTMF: -100, wantTXF: true, wantPartial: false,
		},
		{
			// The load-bearing case: the newly added third POST must
			// not make the existing minis more fragile.
			name: "TXF fails", fail: []string{"TXF"}, wantFound: true,
			wantMXF: -300, wantTMF: -100, wantTXF: false, wantPartial: true,
		},
		{
			name: "MXF fails", fail: []string{"MXF"}, wantFound: true,
			wantMXF: 0, wantTMF: -100, wantTXF: true, wantPartial: true,
		},
		{
			name: "TMF fails", fail: []string{"TMF"}, wantFound: true,
			wantMXF: -300, wantTMF: 0, wantTXF: true, wantPartial: true,
		},
		{
			name: "MXF+TXF fail", fail: []string{"MXF", "TXF"}, wantFound: true,
			wantMXF: 0, wantTMF: -100, wantTXF: false, wantPartial: true,
		},
		{
			name: "TMF+TXF fail", fail: []string{"TMF", "TXF"}, wantFound: true,
			wantMXF: -300, wantTMF: 0, wantTXF: false, wantPartial: true,
		},
		{
			// Both minis down: no error (TXF survived) but found=false,
			// because found is mini-driven — the walk-back should keep
			// looking for a day with renderable 散戶 rows.
			name: "MXF+TMF fail", fail: []string{"MXF", "TMF"}, wantFound: false,
		},
		{
			name: "all fail", fail: []string{"MXF", "TMF", "TXF"}, wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			responses := make(map[string]taifexResponse, len(ok))
			for k, v := range ok {
				responses[k] = v
			}
			for _, c := range tc.fail {
				responses[c+"@"+day] = boom
			}
			p := exactTestProvider(t, responses)

			asOf := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
			got, found, err := p.FetchTAIFEXFuturesExact(context.Background(), asOf)
			switch {
			case tc.wantErr && err == nil:
				t.Fatal("err = nil, want an error (all three contracts failed)")
			case !tc.wantErr && err != nil:
				t.Fatalf("err = %v, want nil (a surviving contract must still be delivered)", err)
			}
			if tc.wantErr {
				return
			}
			if found != tc.wantFound {
				t.Errorf("found = %v, want %v", found, tc.wantFound)
			}
			if !tc.wantFound {
				return
			}
			if got.MXFNet != tc.wantMXF {
				t.Errorf("MXFNet = %d, want %d", got.MXFNet, tc.wantMXF)
			}
			if got.TMFNet != tc.wantTMF {
				t.Errorf("TMFNet = %d, want %d", got.TMFNet, tc.wantTMF)
			}
			if got.HasTXF() != tc.wantTXF {
				t.Errorf("HasTXF() = %v, want %v", got.HasTXF(), tc.wantTXF)
			}
			// Partial is what stops the cache pinning this degraded
			// answer for a full session.
			if got.Partial != tc.wantPartial {
				t.Errorf("Partial = %v, want %v", got.Partial, tc.wantPartial)
			}
		})
	}
}

// TestFetchTAIFEXFuturesExactTXFOnlyNotFound pins the mini-driven
// found semantics: a day where only TXF published is HasAny()-false,
// so found must stay false and let the caller's walk-back continue to
// a day with mini data.
func TestFetchTAIFEXFuturesExactTXFOnlyNotFound(t *testing.T) {
	p := exactTestProvider(t, map[string]taifexResponse{
		"TXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "臺股期貨", -12083, 2214, 35916)},
	})

	day := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	_, found, err := p.FetchTAIFEXFuturesExact(context.Background(), day)
	if err != nil {
		t.Fatalf("FetchTAIFEXFuturesExact: %v", err)
	}
	if found {
		t.Error("found = true, want false (TXF alone must not terminate the walk-back)")
	}
}

// TestCachedRetailFuturesWalksPastTXFOnlyDay is the end-to-end
// consequence of the mini-driven found flag: the cache's walk-back
// steps over a TXF-only day and resolves on the earlier day that has
// mini data.
func TestCachedRetailFuturesWalksPastTXFOnlyDay(t *testing.T) {
	srv := newTAIFEXServer(t, map[string]taifexResponse{
		// Friday: TXF published, minis did not.
		"TXF@2026/04/24": {csvUTF8: taifexCSV("2026/04/24", "臺股期貨", -12083, 2214, 35916)},
		// Thursday: full set.
		"MXF@2026/04/23": {csvUTF8: taifexCSV("2026/04/23", "小型臺指期貨", 100, 0, 200)},
		"TMF@2026/04/23": {csvUTF8: taifexCSV("2026/04/23", "微型臺指期貨", 50, 0, 50)},
		"TXF@2026/04/23": {csvUTF8: taifexCSV("2026/04/23", "臺股期貨", -1, 2, 3)},
	})
	inner := twse.NewWithEndpoints("", "", "", srv.Client())
	inner.SetTAIFEXFuturesEndpoint(srv.URL)
	cached := twse.NewCached(inner)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := cached.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("Cached.GetRetailFutures: %v", err)
	}
	if got.MXFNet != -300 || got.TMFNet != -100 {
		t.Errorf("minis = (%d, %d), want (-300, -100) from the walked-back day", got.MXFNet, got.TMFNet)
	}
	if got.TXFDealerNet != -1 {
		t.Errorf("TXFDealerNet = %d, want -1 (breakdown must come from the resolved day)", got.TXFDealerNet)
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
