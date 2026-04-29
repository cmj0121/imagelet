package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
)

// TestRequestSizeLimiterRejectsLongPath pins that a path exceeding
// MaxRequestPathBytes is short-circuited with HTTP 414 before the
// downstream handler runs.
func TestRequestSizeLimiterRejectsLongPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestSizeLimiter())
	called := false
	r.GET("/*any", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})

	// One byte over the cap: the leading "/" plus MaxRequestPathBytes
	// "a"s gives len(URL.Path) == MaxRequestPathBytes+1.
	path := "/" + strings.Repeat("a", middleware.MaxRequestPathBytes)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestURITooLong)
	}
	if called {
		t.Fatalf("downstream handler ran for oversized path; should have been aborted")
	}
}

// TestRequestSizeLimiterRejectsLongQuery pins that a query string
// exceeding MaxRequestQueryBytes is short-circuited with HTTP 414.
func TestRequestSizeLimiterRejectsLongQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestSizeLimiter())
	called := false
	r.GET("/probe", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})

	query := "x=" + strings.Repeat("a", middleware.MaxRequestQueryBytes)
	req := httptest.NewRequest(http.MethodGet, "/probe?"+query, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestURITooLong)
	}
	if called {
		t.Fatalf("downstream handler ran for oversized query; should have been aborted")
	}
}

// TestRequestSizeLimiterAllowsNormalRequest pins that the limiter is
// transparent on the routes imagelet actually serves: a representative
// /stock?region=tw call must reach the downstream handler.
func TestRequestSizeLimiterAllowsNormalRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestSizeLimiter())
	r.GET("/stock", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/stock?region=tw", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// TestRequestSizeLimiterCapsBody pins that a body larger than
// MaxRequestBodyBytes makes the io.ReadAll on c.Request.Body fail —
// http.MaxBytesReader returns an error once the cap is exceeded.
func TestRequestSizeLimiterCapsBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestSizeLimiter())
	var readErr error
	r.POST("/probe", func(c *gin.Context) {
		_, readErr = io.ReadAll(c.Request.Body)
		c.Status(http.StatusOK)
	})

	body := strings.NewReader(strings.Repeat("a", middleware.MaxRequestBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/probe", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if readErr == nil {
		t.Fatalf("io.ReadAll on oversized body returned nil error; want non-nil from MaxBytesReader")
	}
}
