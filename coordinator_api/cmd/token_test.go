package cmd

import "testing"

func TestTokenPageNote(t *testing.T) {
	cases := []struct {
		first, count, total int
		want                string
	}{
		{0, 100, 3456, "Tokens 1-100 of 3456. Next page: --offset 100"},
		{3400, 56, 3456, "Tokens 3401-3456 of 3456."},
		{0, 3, 3, "Tokens 1-3 of 3."},
		{500, 0, 12, "No tokens on this page (12 in total)."},
	}
	for _, c := range cases {
		if got := tokenPageNote(c.first, c.count, c.total); got != c.want {
			t.Errorf("tokenPageNote(%d, %d, %d) = %q, want %q", c.first, c.count, c.total, got, c.want)
		}
	}
}
