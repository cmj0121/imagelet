package render_test

import (
	"testing"

	"github.com/cmj0121/imagelet/render"
)

func TestStripPylonSyntax(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare_parens", "foo (bar) baz", "foo baz"},
		{"bare_brackets", "foo [bar] baz", "foo baz"},
		{"both", "a (b) c [d] e", "a c e"},
		{"none", "^GSPC USD 21,834.50", "^GSPC USD 21,834.50"},
		{"unmatched_left_paren", "foo (bar", "foo (bar"},
		{"unmatched_right_paren", "foo bar)", "foo bar)"},
		{"unmatched_left_bracket", "foo [bar", "foo [bar"},
		{"caret_safe", "^GSPC", "^GSPC"},
		{"thousands_safe", "21,834.50", "21,834.50"},
		{"middle_dot_safe", "STALE · ^GSPC", "STALE · ^GSPC"},
		{"ampersand_inline", "S&P 500 · United States", "S & P 500 · United States"},
		{"ampersand_spaced", "Tom & Jerry", "Tom & Jerry"},
		{"ampersand_collapses_extra_ws", "A &  B", "A & B"},
		{"ampersand_before_letter_compact", "AT&T 5G", "AT & T 5G"},
		{"ampersand_terminal", "Foo &", "Foo &"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render.StripPylonSyntax(tc.in); got != tc.want {
				t.Errorf("StripPylonSyntax(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
