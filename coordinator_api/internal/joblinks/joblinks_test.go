package joblinks

import (
	"fmt"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	doc := `{"vars": {"a": 1}, "links": [
		{"name": "Build results", "url": "https://builds.example/run/1"},
		{"name": "Plain", "url": "http://ci.example/x"},
		{"name": "Script", "url": "javascript:alert(1)"},
		{"name": "", "url": "https://a.example"},
		{"name": "Creds", "url": "https://user:pw@a.example"},
		{"name": "Build results", "url": "https://dup.example"},
		{"name": "Relative", "url": "/x"}
	]}`
	links, dropped, err := Parse("job-1", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].Name != "Build results" || links[1].Position != 1 || links[0].JobID != "job-1" {
		t.Fatalf("links %+v", links)
	}
	if len(dropped) != 5 {
		t.Fatalf("dropped %v", dropped)
	}
}

func TestParseNoLinksAndBadShape(t *testing.T) {
	if links, dropped, err := Parse("j", []byte(`{"vars": {}}`)); err != nil || links != nil || dropped != nil {
		t.Fatalf("no links key: %v %v %v", links, dropped, err)
	}
	if _, _, err := Parse("j", []byte(`not json`)); err == nil {
		t.Fatal("bad json must fail")
	}
	links, dropped, err := Parse("j", []byte(`{"links": {"name": "x"}}`))
	if err != nil || len(links) != 0 || len(dropped) != 1 {
		t.Fatalf("bad shape: %v %v %v", links, dropped, err)
	}
}

func TestParseLimit(t *testing.T) {
	items := []string{}
	for i := 0; i < MaxLinks+3; i++ {
		items = append(items, fmt.Sprintf(`{"name": "l%d", "url": "https://x.example/%d"}`, i, i))
	}
	links, dropped, _ := Parse("j", []byte(`{"links": [`+strings.Join(items, ",")+`]}`))
	if len(links) != MaxLinks || len(dropped) != 3 {
		t.Fatalf("got %d links, %d dropped", len(links), len(dropped))
	}
	if ValidateLink(strings.Repeat("n", MaxNameLength+1), "https://x.example") == nil {
		t.Fatal("a long name must fail")
	}
}
