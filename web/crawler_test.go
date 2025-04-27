package web

import (
	"context"
	"net/url"
	"testing"

	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestCrawlerVisit(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	ctx := t.Context()
	defer ctrl.Finish()
	mockfetch := NewMockFetcher(ctrl)
	c := Crawler{pf: mockfetch}
	c.Init()
	innerLink := "https://github.com/harrybrwn"
	page := Page{
		Links: []*url.URL{
			must(url.Parse(innerLink)),
		},
	}
	c.OnPage(func(ctx context.Context, p *Page) {
		is.Equal(p, &page)
	})
	c.OnLink(func(link *url.URL) {
		is.Equal(link.String(), innerLink)
	})
	mockfetch.EXPECT().Fetch(
		ctx,
		NewMatcher(
			func(v *PageRequest) bool { return v.URL == "https://hrry.me" && v.Depth == 0 },
			`&PageRequest{"https://hrry.me", 0}`),
	).Return(&page, nil)
	err := c.Visit(ctx, "https://hrry.me")
	is.NoErr(err)
}

func NewMatcher[T any](match func(v T) bool, repr ...string) gomock.Matcher {
	m := matcher[T]{match: match}
	if len(repr) > 0 {
		m.repr = repr[0]
	}
	return &m
}

type matcher[T any] struct {
	match func(T) bool
	repr  string
}

func (m *matcher[T]) String() string { return m.repr }
func (m *matcher[T]) Matches(other any) bool {
	o, ok := other.(T)
	return ok && m.match(o)
}

func must[T any](v T, e error) T {
	if e != nil {
		panic(e)
	}
	return v
}
