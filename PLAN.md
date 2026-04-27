<!-- markdownlint-disable MD013 MD040 -->

# PLAN.md — Refine /now /stock /weather using post-EAW pylon

## Idea

Refine the three banner-rendered services using new pylon banner-font features (post EAW PR) and richer data sources. /now keeps default font but gets a year-progress bar inline with the date subtitle. /stock gets index-name header, currency, day-range bar, 52w-range bar, and TW-only 三大法人 + margin balance enrichment. /weather adds humidity/UV/precipitation captions, day-cycle progress bar, and globally-applicable AQI + recent-earthquake enrichment.

## Design

### /now

- **Drop colon-substitution workaround** in `render/banner.go` (default font now has `:` glyph in v0.2 pylon).
- **Add year-progress bar** inline with the date subtitle (option C — single subtitle box):
  - Format: `2026-04-27 Mon UTC+8 · year ██████░░░░░░░░░░░░░░ 32%`
  - Bar is 20 cells, `█` filled by `floor(day_of_year / days_in_year * 20)`, `░` empty.
- Banner stays default ANSI shadow.

### /stock

- **Index name + region header** — hardcoded map in stock.go: `^TWII → TAIEX · Taiwan`, `^GSPC → S&P 500 · United States`, `^N225 → Nikkei 225 · Japan`, `^HSI → Hang Seng · Hong Kong`, `^FTSE → FTSE 100 · United Kingdom`, `^GDAXI → DAX · Germany`. Default fallback uses bare symbol.
- **Currency** — display `meta.currency` (already on `quote.Quote`) in caption: `^TWII  ▲ +0.42%  21,834.50 TWD  2026-04-26`.
- **Day-range + 52w-range bars** — extend `quote.Quote` with `DayHigh DayLow Week52High Week52Low float64`; Yahoo parser fills from `meta.regularMarketDayHigh/Low` and `meta.fiftyTwoWeekHigh/Low`. Bars are 20 cells; missing data → bar omitted (no error).
- **TWSE provider** (TW-only) at `service/stock/twse/twse.go`:
  - 三大法人 aggregate: `https://www.twse.com.tw/rwd/zh/fund/BFI82U?dayDate=YYYYMMDD&type=day&response=json`
  - Margin balance: `https://www.twse.com.tw/rwd/zh/marginTrading/MI_MARGN?date=YYYYMMDD&selectType=MS&response=json`
  - Both key-free, no auth, JSON response. UA-spoof to `Mozilla/5.0` (defensive).
  - Cache TTL: 4h success, 30min failure (data updates ~16:00 daily TST).
  - `TWMarketData{InstitutionalNetBuyTWD, ForeignNetBuyTWD, TrustNetBuyTWD, DealerNetBuyTWD, MarginLongBalanceTWD, MarginShortBalanceLots, AsOf time.Time}`.
- **Layout (V1 single outer box + visible thin divider)**:

  ```
  ┌──────────────────────────────────────────────────────────────────┐
  │                          TAIEX · Taiwan                          │
  │            ^TWII  ▲ +0.42%  21,834.50 TWD  2026-04-26            │
  │                   day ███████░░░░░░░░░░░░░ 38%                   │
  │                   52w ███████████████░░░░░ 78%                   │
  │   ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─    │
  │   三大法人  外資 +43.9B  投信 +2.2B  自營 +8.9B  合計 ▲ +55.0B   │
  │       融資餘額 4,409億 TWD          融券餘額 19萬張             │
  └──────────────────────────────────────────────────────────────────┘
  ```

- **Path 1 region-conditional CN/EN labels**:
  - ASCII surface (curl/terminal): CN labels (`三大法人`, `外資`, `投信`, `自營`, `合計`, `融資餘額`, `融券餘額`).
  - PNG surface (browser): EN equivalents (`institutional`, `foreign`, `trust`, `dealer`, `net`, `margin long`, `margin short`).
  - Pylon's PNG embedded font has zero CJK coverage — verified by smith. EN fallback prevents tofu.

### /weather

- **Banner font** → `banner:mini` (compact 1-stroke, balances ASCII condition icon's height).
- **Additional captions** — humidity %, UV index (rounded), precipitation today (mm). Source: Open-Meteo (already in our daily/current response, just thread three more fields through `forecast.Forecast`).
- **Day-cycle progress bar** — visualizes "now" between sunrise and sunset:
  - Format: `daylight ██████████░░░░░░░░░░ 50%` (or `night ░░░░░██████░░░░░░░░░ %` post-sunset).
  - 20 cells. Bar position = `(now - sunrise) / (sunset - sunrise)` clamped [0,1].
  - Polar latitudes (no sunrise/sunset some days): bar omitted gracefully.
- **AQI + recent-earthquake enrichment** (works for ALL regions, not TW-only):

  - AQI: Open-Meteo Air Quality API at `air-quality-api.open-meteo.com/v1/air-quality?latitude=X&longitude=Y&current=pm2_5,european_aqi,us_aqi&timezone=...`. Key-free, global.
  - Earthquakes: USGS at `earthquake.usgs.gov/fdsnws/event/1/query?format=geojson&minlatitude=...&minmagnitude=3&starttime=...`. Key-free, global. Bounds box ≈ ±2.5° around visitor lat/lon, last 24h, magnitude ≥ 3.
  - Caption examples:

    ```
    AQI 74 (US) · PM2.5 22 μg/m³
    quakes (24h)  ·  M3.8 50km E of Hualien
    ```

- **Path 1 CN/EN** for the AQI/quake captions on TW path:
  - CN: `空氣品質 74` / `近期地震 24h`
  - EN: `AQI 74` / `quakes 24h`
- **Layout (V1 single outer box, mirrors /stock)**:

  ```
  ┌─────────────────┐  ┌──────────────────────────┐
  │  [icon 5 rows]  │  │      [mini banner]        │
  └─────────────────┘  └──────────────────────────┘
  ┌──────────────────────────────────────────────────────────────────┐
  │                       SUNNY in Taipei (TW)                       │
  │            feels 23°C  wind 12 km/h                              │
  │            high 26°C / low 19°C                                  │
  │            humidity 68%  UV 7  precipitation 0 mm today          │
  │            sunrise 5:42  sunset 18:21                            │
  │            daylight ██████████░░░░░░░░░░ 50%                     │
  │   ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─    │
  │   空氣品質 74 (US)  PM2.5 22 μg/m³                               │
  │   近期地震 24h  M3.8 50km E of Hualien                           │
  └──────────────────────────────────────────────────────────────────┘
  ```

  - Note: PNG path captions sanitized (existing `pngCaptionSanitizer` already strips `°` and `·`); CN labels swapped to EN for PNG.

### Common renderer concerns

- **Bar character set**: `█` filled, `░` empty for both default and ascii themes; pylon's text-content rendering doesn't substitute these (only banner glyph rows get `█→#`). Both render natively in PNG via pylon's font.
- **Path 1 abstraction**: services build CN+EN caption pairs at composition time, branch on `mode == ModeASCII` / `mode == ModePNG` at the renderer entry point.
- **Visible thin divider**: `─ ─ ─ ─ …` (alternating `─` + space) reads as a section break in both surfaces. Not a true blank line but cleaner than ZWSP, which causes a PNG width-measurement artifact.
- **Graceful absence**: every new datum (52w, AQI, quakes, day-cycle) MUST handle missing data without erroring — bar/caption omitted.

## Spec

Skipped. Designs are mechanical translation of locked picks; no API contracts or schemas need formal spec.

## Units of Work

| #   | Unit                                        | Description                                                                                                                                                   | Assignee | Depends On   | Status  |
| --- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------------ | ------- |
| 1   | render: drop colon substitution             | Delete `strings.ReplaceAll(":", " ")` in `render/banner.go`; rename related test pin to assert `:` glyph rows present                                         | hale     | —            | pending |
| 2   | now: year-progress bar                      | New `render.YearProgress(t time.Time) string` returning `year ██░░░░░░░░░ 32%` style; thread into now.go subtitle (option C: inline with date)                | hale     | 1            | pending |
| 3   | stock: index name + currency thread-through | hardcoded index map; quote.Quote.Currency already exists, just emit in caption                                                                                | hale     | —            | pending |
| 4   | stock: 52w + day-range bars                 | quote.Quote +4 fields (DayHigh/DayLow/Week52High/Week52Low), yahoo parser, render helper, graceful absence                                                    | hale     | 3            | pending |
| 5   | stock: TWSE provider                        | service/stock/twse package; BFI82U + MI_MARGN fetchers; cached wrapper (4h success / 30min failure TTL) with singleflight; httptest+golden fixtures; UA spoof | hale     | —            | pending |
| 6   | stock: V1 layout + Path 1 CN/EN             | rewrite stock renderer to V1 single-box; build CN+EN caption variants; mode-conditional pylon source assembly                                                 | hale     | 3, 4, 5      | pending |
| 7   | weather: switch to banner:mini              | one-line headlineSource change                                                                                                                                | hale     | —            | pending |
| 8   | weather: humidity/UV/precipitation captions | forecast.Forecast +3 fields, openmeteo parser update, weather.go captions                                                                                     | hale     | —            | pending |
| 9   | weather: day-cycle bar                      | render helper computing bar from sunrise/sunset/now; clamp polar edge cases                                                                                   | hale     | 8            | pending |
| 10  | weather: AQI provider                       | service/weather/airquality package (Open-Meteo air-quality); cached wrapper (1h TTL); httptest fixtures                                                       | hale     | —            | pending |
| 11  | weather: earthquake provider                | service/weather/quake package (USGS); cached wrapper (15min TTL — quakes change frequently); httptest fixtures                                                | hale     | —            | pending |
| 12  | weather: V1 layout + Path 1 CN/EN           | rewrite weather renderer; mode-conditional CN+EN; integrate AQI + quakes (only when data present)                                                             | hale     | 8, 9, 10, 11 | pending |
| 13  | docs: README updates                        | Document new captions, bars, regional behavior, /weather and /stock visual changes                                                                            | twain    | 1-12         | pending |
| 14  | ops: page review                            | Reliability/observability review on the 4 new external integrations + the visualization layer                                                                 | page     | 1-13         | pending |

## Planned Commits

| #   | Commit                                                                             | Description |
| --- | ---------------------------------------------------------------------------------- | ----------- |
| 1   | `refactor(render): drop banner colon substitution; default font now has ':' glyph` | unit 1      |
| 2   | `feat(now): add year-progress bar to date subtitle`                                | unit 2      |
| 3   | `feat(stock): index name map + currency in caption`                                | unit 3      |
| 4   | `feat(stock): 52w-range and day-range progress bars`                               | unit 4      |
| 5   | `feat(stock): TWSE provider for 三大法人 + margin balance (TW-only)`               | unit 5      |
| 6   | `feat(stock): V1 single-box layout + Path 1 region-conditional CN/EN labels`       | unit 6      |
| 7   | `refactor(weather): switch banner to mini font`                                    | unit 7      |
| 8   | `feat(weather): humidity/UV/precipitation captions from Open-Meteo`                | unit 8      |
| 9   | `feat(weather): day-cycle progress bar (sunrise→now→sunset)`                       | unit 9      |
| 10  | `feat(weather): AQI from Open-Meteo Air Quality API (key-free, global)`            | unit 10     |
| 11  | `feat(weather): recent earthquake from USGS (key-free, global)`                    | unit 11     |
| 12  | `feat(weather): V1 layout + Path 1 region-conditional CN/EN labels`                | unit 12     |
| 13  | `docs(readme): document refined views, new captions, and data sources`             | unit 13     |

## Risks (post tenth-man + pre-flight verification)

1. **TWSE legacy paths** (BFI82U, MI_MARGN) are technically undocumented (not in openapi.twse.com.tw swagger) but stable per public observation. Mitigation: behind `twse.Provider` interface — single-file swap if breakage.
2. **TWSE response uses ROC year format** (date `1150424` = ROC 115 = 2026). Parser must convert. Pin test fixtures.
3. **Yahoo's 52w fields** (`fiftyTwoWeekHigh`/`Low`) sometimes missing for thinly-traded indices. Bar absent gracefully (no error, just don't render that row).
4. **Polar latitudes** (no sunrise/sunset on some days) crash naive `(now-sunrise)/(sunset-sunrise)` math. Clamp via `if sunset == sunrise { skip bar }`.
5. **USGS earthquake bounds** — picking ±2.5° lat/lon arbitrarily. May miss meaningful far-field events (e.g., Pacific tsunami precursors). Trade-off accepted.
6. **Path 1 maintenance burden** — every new TW caption needs CN+EN strings. For v1, hand-coded `if mode == ModeASCII { CN } else { EN }` blocks. Refactor to `i18n.T()` only if a third surface is ever added.
7. **CN-rendering on terminals without CJK fonts** — rare in 2026 but possible. ASCII path silently falls back to terminal's missing-glyph behavior. Acceptable.
8. **Single-branch scope ~860 LOC** — accepted by user. Twelve units, sequential merges.

## Iteration Log

| Iteration | Correctness | Completeness | Quality | Test Coverage | Summary |
| --------- | ----------- | ------------ | ------- | ------------- | ------- |

(Filled in during Phase 5.)
