package web

import (
	"bytes"
	"context"
	"hash/fnv"
	"io"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

type vis struct {
	filter    func(*PageRequest, *url.URL) error
	visit     func(context.Context, *Page)
	linkFound func(*url.URL)
}

func (v *vis) Filter(p *PageRequest, u *url.URL) error { return v.filter(p, u) }
func (v *vis) Visit(ctx context.Context, p *Page)      { v.visit(ctx, p) }
func (v *vis) LinkFound(u *url.URL)                    { v.linkFound(u) }

func newvis() *vis {
	return &vis{
		filter:    func(*PageRequest, *url.URL) error { return nil },
		visit:     func(context.Context, *Page) {},
		linkFound: func(*url.URL) {},
	}
}

func TestCrawler(t *testing.T) {
	t.Skip()
	var (
		v = newvis()
		n int64
	)
	log.SetLevel(logrus.FatalLevel)
	v.filter = func(p *PageRequest, u *url.URL) error { return nil }
	v.visit = func(ctx context.Context, p *Page) { atomic.AddInt64(&n, 1) }

	var (
		ctx     = context.Background()
		db      = inMemDB(t)
		crawler = NewCrawler(
			WithDB(db),
			WithLimit(2),
			WithQueueSize(500),
			WithSleep(0),
			WithVisitor(v),
		)
	)
	defer crawler.Close()
	p := ParsePageRequest("https://quotes.toscrape.com/", 0)
	crawler.Enqueue(p)

	ctx, cancel := context.WithTimeout(ctx, time.Second*2)
	defer cancel()
	crawler.wg.Add(1)
	go crawler.Crawl(ctx, 10)
	<-ctx.Done()
}

var testlinks = []string{
	"https://ar.wikipedia.org/wiki/%D9%85%D9%88%D8%B3%D9%88%D8%B9%D8%A9",
	"https://quotes.toscrape.com/",
	"https://en.wikipedia.org/wiki/16th_International_Emmy_Awards",
	"https://en.wikipedia.org/wiki/16th_Japan_Film_Professional_Awards",
	"https://en.wikipedia.org/wiki/16th_Japan_Record_Awards",
	"https://en.wikipedia.org/wiki/16th_Legislative_Assembly_of_Puerto_Rico",
	"https://en.wikipedia.org/wiki/16th_Lok_Sabha",
	"https://en.wikipedia.org/wiki/16th_Mechanized_Infantry_Division_(Greece)",
	"https://en.wikipedia.org/wiki/16th_National_Film_Awards",
	"https://en.wikipedia.org/wiki/16th_National_Hockey_League_All-Star_Game",
	"https://en.wikipedia.org/wiki/16th_National_Television_Awards",
	"https://en.wikipedia.org/wiki/16th_Oklahoma_Legislature",
	"https://en.wikipedia.org/wiki/16th_Panzer_Division_(Wehrmacht)",
	"https://en.wikipedia.org/wiki/16th_Parachute_Brigade",
	"https://en.wikipedia.org/wiki/16th_Parachute_Brigade_(United_Kingdom)",
	"https://en.wikipedia.org/wiki/16th_parallel_north",
	"https://en.wikipedia.org/wiki/16th_Politburo_of_the_Chinese_Communist_Party",
	"https://en.wikipedia.org/wiki/16th_Primetime_Emmy_Awards",
	"https://en.wikipedia.org/wiki/16th_Rifle_Division_(Soviet_Union)",
	"https://en.wikipedia.org/wiki/16th_Robert_Awards",
	"https://en.wikipedia.org/wiki/16th_Santosham_Film_Awards",
	"https://en.wikipedia.org/wiki/16th_Satellite_Awards",
	"https://en.wikipedia.org/wiki/16th_Screen_Actors_Guild_Awards",
	"https://en.wikipedia.org/wiki/16th_Space_Control_Squadron",
	"https://en.wikipedia.org/wiki/16th_SS_Panzergrenadier_Division_Reichsf%C3%BChrer-SS",
	"https://en.wikipedia.org/wiki/16th_Street_Baptist_Church",
	"https://en.wikipedia.org/wiki/16th_Street_Baptist_Church_bombing",
	"https://en.wikipedia.org/wiki/16th_Street_Baptist_Church_Bombing",
	"https://en.wikipedia.org/wiki/16th_Street_Mission_(BART_station)",
	"https://en.wikipedia.org/wiki/16th_Street_NW",
	"https://en.wikipedia.org/wiki/16th_Summit_of_the_Non-Aligned_Movement",
	"https://en.wikipedia.org/wiki/16th_Tamil_Nadu_Assembly",
	"https://en.wikipedia.org/wiki/16th_The_Queen%27s_Lancers",
	"https://en.wikipedia.org/wiki/16th_TVyNovelas_Awards",
	"https://en.wikipedia.org/wiki/16th_Uttar_Pradesh_Assembly",
	"https://en.wikipedia.org/wiki/16_Tons",
	"https://en.wikipedia.org/wiki/16_Vayathinile",
	"https://en.wikipedia.org/wiki/16_Volt",
	"https://en.wikipedia.org/wiki/16_Vulpeculae",
}

func TestVisitedSet(t *testing.T) {
	c := NewCrawler()
	defer c.Close()
	defer c.DB.Close()
	links := testlinks[:]
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		err = c.markvisited(*u)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := c.urlSeen(*u)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("url %q should be marked as visited", l)
		}
	}
}

func TestURLKey(t *testing.T) {
	link := "https://ar.wikipedia.org/wiki/%D9%85%D9%88%D8%B3%D9%88%D8%B9%D8%A9"
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("visited_" + link)
	got := urlKey(u)
	if !bytes.Equal(got, want) {
		t.Errorf("wrong result: got %q, want %q", got, want)
	}
}

func BenchmarkHash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, l := range testlinks {
			h := fnv.New128()
			io.WriteString(h, l)
			h.Reset()
		}
	}
}
