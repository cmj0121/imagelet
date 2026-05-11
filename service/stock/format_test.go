package stock_test

import (
	"testing"

	"github.com/cmj0121/imagelet/service/stock"
)

func TestFormatThousands(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-1000, "-1,000"},
		{-1234567, "-1,234,567"},
		{1000000, "1,000,000"},
	}
	for _, tc := range cases {
		got := stock.FormatThousandsForTest(tc.in)
		if got != tc.want {
			t.Errorf("FormatThousands(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatLargeNumber(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_000, "1.0K"},
		{1_500, "1.5K"},
		{1_000_000, "1.0M"},
		{1_500_000, "1.5M"},
		{1_000_000_000, "1.0B"},
		{2_500_000_000, "2.5B"},
		{1_000_000_000_000, "1.0T"},
		{-1_000_000, "-1.0M"},
	}
	for _, tc := range cases {
		got := stock.FormatLargeNumberForTest(tc.in)
		if got != tc.want {
			t.Errorf("FormatLargeNumber(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestRocYearMonthLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"11503", "2026/03"},
		{"11412", "2025/12"},
		{"11501", "2026/01"},
		{"10801", "2019/01"},
		// edge cases
		{"", ""},
		{"abc", ""},
		{"11500", ""},    // month 00 invalid
		{"11513", ""},    // month 13 invalid
		{"  11503  ", "2026/03"}, // trims whitespace
	}
	for _, tc := range cases {
		got := stock.RocYearMonthLabelForTest(tc.in)
		if got != tc.want {
			t.Errorf("RocYearMonthLabel(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
