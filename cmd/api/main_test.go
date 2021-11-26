package main

import (
	"bytes"
	"strings"
	"testing"
)

func Test(t *testing.T) {}

func TestListQueryBuilder(t *testing.T) {
	var b bytes.Buffer
	tests := []struct {
		p        ListParams
		contains []string
		results  int
	}{
		{
			p:        ListParams{Limit: 23},
			results:  1,
			contains: []string{"LIMIT $1"},
		},
		{
			p:        ListParams{Host: "site.com", OrderBy: "url"},
			results:  2,
			contains: []string{"LIKE '%' || $1 || '%'", "ORDER BY $2"},
		},
		{
			p:        ListParams{Host: "site.com", OrderBy: "url", Limit: 12},
			results:  3,
			contains: []string{"LIKE '%' || $1 || '%'", "ORDER BY $2", "LIMIT $3"},
		},
		{
			p:        ListParams{OrderBy: "crawled_at"},
			results:  1,
			contains: []string{"ORDER BY $1"},
		},
		{
			p:        ListParams{OrderBy: "crawled_at", Reverse: true},
			results:  1,
			contains: []string{"ORDER BY $1 DESC"},
		},
		{
			results: 0,
		},
	}
	for _, tst := range tests {
		b.Reset()
		params, err := buildListQueryParameters(&b, &tst.p)
		if err != nil {
			t.Error(err)
			continue
		}
		if len(params) != tst.results {
			t.Errorf("expected result of length %d, got one of length %d", tst.results, len(params))
			continue
		}
		q := b.String()
		for _, s := range tst.contains {
			if !strings.Contains(q, s) {
				t.Errorf("resulting query %q should contain %q but did not", q, s)
			}
		}
	}
}
