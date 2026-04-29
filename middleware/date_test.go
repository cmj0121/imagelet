package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
)

func TestDateOverrideDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		dateQuery string
		cfTZ      string
		wantSet   bool
		wantYear  int
		wantMonth time.Month
		wantDay   int
		wantZone  string
	}{
		{"no_query", "", "", false, 0, 0, 0, ""},
		{"valid_date", "2012-02-02", "", true, 2012, time.February, 2, time.Local.String()},
		{"valid_with_tz", "2012-02-02", "Asia/Taipei", true, 2012, time.February, 2, "Asia/Taipei"},
		{"invalid_format", "not-a-date", "", false, 0, 0, 0, ""},
		{"out_of_range", "2012-13-99", "", false, 0, 0, 0, ""},
		{"timestamp_not_accepted", "1325462400", "", false, 0, 0, 0, ""},
		{"rfc3339_not_accepted", "2012-02-02T00:00:00Z", "", false, 0, 0, 0, ""},
		{"empty_string", "", "", false, 0, 0, 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.TimezoneDetector())
			r.Use(middleware.DateOverrideDetector())

			var (
				gotSet  bool
				gotTime time.Time
			)
			r.GET("/probe", func(c *gin.Context) {
				gotTime, gotSet = middleware.GetDate(c)
				c.Status(http.StatusOK)
			})

			path := "/probe"
			if tc.dateQuery != "" {
				path += "?date=" + tc.dateQuery
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.cfTZ != "" {
				req.Header.Set("CF-Timezone", tc.cfTZ)
			}
			r.ServeHTTP(httptest.NewRecorder(), req)

			if gotSet != tc.wantSet {
				t.Fatalf("set = %v, want %v", gotSet, tc.wantSet)
			}
			if !tc.wantSet {
				return
			}
			if gotTime.Year() != tc.wantYear || gotTime.Month() != tc.wantMonth || gotTime.Day() != tc.wantDay {
				t.Errorf("date = %v, want %d-%02d-%02d", gotTime, tc.wantYear, int(tc.wantMonth), tc.wantDay)
			}
			if zone := gotTime.Location().String(); zone != tc.wantZone {
				t.Errorf("location = %q, want %q", zone, tc.wantZone)
			}
		})
	}
}

func TestNowForWithoutOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	loc, _ := time.LoadLocation("Asia/Taipei")

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())

	var got time.Time
	r.GET("/probe", func(c *gin.Context) {
		got = middleware.NowFor(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("CF-Timezone", "Asia/Taipei")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if got.Location().String() != loc.String() {
		t.Errorf("location = %q, want %q", got.Location(), loc)
	}
	now := time.Now().In(loc)
	if got.Year() != now.Year() || got.Month() != now.Month() || got.Day() != now.Day() {
		t.Errorf("date = %v, want today (%v)", got.Format("2006-01-02"), now.Format("2006-01-02"))
	}
}

func TestNowForWithOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	loc, _ := time.LoadLocation("Asia/Taipei")

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())

	var got time.Time
	r.GET("/probe", func(c *gin.Context) {
		got = middleware.NowFor(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe?date=2012-02-02", nil)
	req.Header.Set("CF-Timezone", "Asia/Taipei")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if got.Year() != 2012 || got.Month() != time.February || got.Day() != 2 {
		t.Errorf("date = %v, want 2012-02-02", got.Format("2006-01-02"))
	}
	if got.Location().String() != loc.String() {
		t.Errorf("location = %q, want %q", got.Location(), loc)
	}
	now := time.Now().In(loc)
	// Wall-clock H/M should match real now (allow 1-min skew for test latency).
	delta := now.Sub(time.Date(now.Year(), now.Month(), now.Day(), got.Hour(), got.Minute(), got.Second(), got.Nanosecond(), loc))
	if delta < 0 {
		delta = -delta
	}
	if delta > 2*time.Minute {
		t.Errorf("wall-clock drift > 2m: now=%v got-time=%02d:%02d:%02d",
			now.Format("15:04:05"), got.Hour(), got.Minute(), got.Second())
	}
}

func TestDateOverrideRejectsTooOld(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())

	var gotSet bool
	r.GET("/probe", func(c *gin.Context) {
		_, gotSet = middleware.GetDate(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe?date=1990-01-01", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if gotSet {
		t.Errorf("GetDate set = true for date=1990-01-01, want false (out of range)")
	}
}

func TestDateOverrideRejectsTooFuture(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())

	var gotSet bool
	r.GET("/probe", func(c *gin.Context) {
		_, gotSet = middleware.GetDate(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe?date=2050-01-01", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if gotSet {
		t.Errorf("GetDate set = true for date=2050-01-01, want false (out of range)")
	}
}

func TestDateOverrideAcceptsRecentPast(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())

	var gotSet bool
	r.GET("/probe", func(c *gin.Context) {
		_, gotSet = middleware.GetDate(c)
		c.Status(http.StatusOK)
	})

	// 9 years ago — comfortably inside the ±10y window.
	target := time.Now().AddDate(-9, 0, 0).Format("2006-01-02")
	req := httptest.NewRequest(http.MethodGet, "/probe?date="+target, nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if !gotSet {
		t.Errorf("GetDate set = false for date=%s (9y ago), want true", target)
	}
}

func TestGetDateWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if _, ok := middleware.GetDate(c); ok {
		t.Errorf("GetDate without middleware = true, want false")
	}
}

func TestNowForWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	got := middleware.NowFor(c)
	if got.Location() != time.Local {
		t.Errorf("NowFor without middleware location = %v, want time.Local", got.Location())
	}
	now := time.Now()
	if got.Year() != now.Year() || got.YearDay() != now.YearDay() {
		t.Errorf("NowFor without middleware = %v, want today (%v)",
			got.Format("2006-01-02"), now.Format("2006-01-02"))
	}
}
