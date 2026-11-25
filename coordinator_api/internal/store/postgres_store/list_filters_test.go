package postgres_store

import "testing"

func TestLikePattern(t *testing.T) {
	cases := []struct {
		pattern   string
		substring bool
		want      string
	}{
		{"todpunk/*", false, "todpunk/%"},
		{"main", false, "main"},
		{"build", true, "%build%"},
		{"build*", true, "build%"},
		{"v?.1", false, "v_.1"},
		{"100%_done\\", true, `%100\%\_done\\%`},
	}
	for _, c := range cases {
		if got := likePattern(c.pattern, c.substring); got != c.want {
			t.Errorf("likePattern(%q, %v) = %q, want %q", c.pattern, c.substring, got, c.want)
		}
	}
}
