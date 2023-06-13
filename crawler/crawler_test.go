package crawler

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
	"github.com/matryer/is"
	"github.com/sirupsen/logrus"
)

var logger = logrus.New()

func init() {
	// logger.Level = logrus.WarnLevel
	logger.Level = logrus.DebugLevel
}

var testPage = &web.Page{
	URL: mustParse("https://en.wikipedia.org/wiki/Main_Page"),
	Links: []*url.URL{
		mustParse("https://en.wikipedia.org/wiki/Umberto_Eco"),
		mustParse("https://en.wikipedia.org/wiki/Portal:Science"),
		mustParse("https://en.wikipedia.org/wiki/COVID-19_pandemic"),
		mustParse("https://en.wikipedia.org/wiki/Leftist_tree"),
	},
	Depth: 1,
}

func Test(t *testing.T) {
	t.Skip()
	is := is.New(t)
	page := &web.Page{
		URL: mustParse("https://en.wikipedia.org/wiki/Main_Page"),
		Links: []*url.URL{
			mustParse("https://en.wikipedia.org/wiki/Umberto_Eco"),
			mustParse("https://en.wikipedia.org/wiki/Portal:Science"),
			mustParse("https://en.wikipedia.org/wiki/COVID-19_pandemic"),
		},
		Depth: 1,
	}
	var (
		ctx = context.Background()
	)
	bus := event.NewChannelBus()
	consumer, err := bus.Consumer("e4e4.en.wikipedia.org")
	is.NoErr(err)
	eventPub, err := bus.Publisher()
	is.NoErr(err)
	urlset := storage.NewInMemoryURLSet()
	c := Crawler{
		Host:       "en.wikipedia.org",
		URLSet:     urlset,
		Fetcher:    &mockFetcher{Page: page},
		RobotsCtrl: &allowAllRobotsCtrl{},
		Consumer:   consumer,
		Publisher: &PagePublisher{
			Publisher: eventPub,
			Robots:    &allowAllRobotsCtrl{},
			Logger:    logger,
		},
		Visitor:    &logVisitor{logger.WithField("visitor", "*logVisitor")},
		Wait:       time.Millisecond,
		DepthLimit: 3,
		Logger:     logger,
	}
	done := make(chan struct{})
	go func() {
		is.NoErr(c.Publisher.Publish(context.Background(), 0, []*url.URL{page.URL}))
		time.Sleep(time.Millisecond * 25)
		done <- struct{}{}
	}()
	err = c.Start(ctx)
	is.NoErr(err)
	<-done

	for _, u := range []*url.URL{
		mustParse("https://en.wikipedia.org/wiki/Main_Page"),
		mustParse("https://en.wikipedia.org/wiki/Umberto_Eco"),
		mustParse("https://en.wikipedia.org/wiki/Portal:Science"),
		mustParse("https://en.wikipedia.org/wiki/COVID-19_pandemic"),
	} {
		if !c.URLSet.Has(ctx, u) {
			t.Errorf("urlset does not have %v", u)
		}
	}
	page = web.NewPageFromString("https://en.wikipedia.org/wiki/Test_Page", 0)
	c.Fetcher = &mockFetcher{page}
	request := web.NewPageRequest(page.URL, 0)
	_, err = c.handle(ctx, request)
	is.NoErr(err)
	is.True(c.URLSet.Has(ctx, page.URL))
	is.NoErr(c.Stop())
	is.True(!c.Running())
}

func TestCrawler(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	bus := event.NewChannelBus()
	eventPub, err := bus.Publisher()
	is.NoErr(err)
	consumer, err := bus.Consumer("e4e4.en.wikipedia.org")
	is.NoErr(err)
	urlset := storage.NewInMemoryURLSet()
	visitor := timingVisitor{times: make(chan time.Time)}
	c := Crawler{
		Host:       "en.wikipedia.org",
		URLSet:     urlset,
		Fetcher:    &mockFetcher{Page: testPage},
		RobotsCtrl: &allowAllRobotsCtrl{},
		Consumer:   consumer,
		Publisher: &PagePublisher{
			Publisher: eventPub,
			Robots:    &allowAllRobotsCtrl{},
			Logger:    logger,
		},
		Visitor:    &visitor,
		Wait:       time.Millisecond * 500,
		DepthLimit: 3,
		Logger:     logger,
	}
	is.NoErr(c.Start(ctx))

	// Collect visit times and calculate the difference between them
	times := make([]time.Time, 0)
	go func() {
		for tm := range visitor.times {
			times = append(times, tm)
		}
	}()
	go c.Publisher.Publish(ctx, 0, []*url.URL{testPage.URL})
	time.Sleep(time.Second * 2)
	close(visitor.times)
	for i := 1; i < len(times); i++ {
		d := times[i].Sub(times[i-1]).Round(10 * time.Millisecond)
		if d != c.Wait {
			t.Errorf("expected visitor to be called around every %v, instead got %v", c.Wait, d)
		}
	}
}

type mockFetcher struct {
	Page *web.Page
}

func (mf *mockFetcher) Fetch(ctx context.Context, req *web.PageRequest) (*web.Page, error) {
	if req.URL == mf.Page.URL.String() {
		return mf.Page, nil
	}
	return &web.Page{URL: mustParse(req.URL)}, nil
}

func mustParse(urlstr string) *url.URL {
	u, err := url.Parse(urlstr)
	if err != nil {
		panic(err)
	}
	return u
}

type allowAllRobotsCtrl struct{}

func (*allowAllRobotsCtrl) ShouldSkip(*url.URL) bool { return false }

type logVisitor struct{ logger logrus.FieldLogger }

func (v *logVisitor) LinkFound(u *url.URL) error {
	v.logger.WithField("stage", "link-found").Debug(u)
	return nil
}

func (v *logVisitor) Filter(_ *web.PageRequest, u *url.URL) error {
	v.logger.WithField("stage", "filter").Info(u)
	return nil
}

func (v *logVisitor) Visit(_ context.Context, p *web.Page) {
	v.logger.WithField("stage", "visit").Info(p.URL)
}

type timingVisitor struct {
	times chan time.Time
}

func (tv *timingVisitor) LinkFound(*url.URL) error                { return nil }
func (tv *timingVisitor) Filter(*web.PageRequest, *url.URL) error { return nil }
func (tv *timingVisitor) Visit(_ context.Context, p *web.Page) {
	tv.times <- time.Now()
}

func TestURLEq(t *testing.T) {
	is := is.New(t)
	a, err := url.Parse("https://en.wikipedia.org/wiki/ZFS")
	is.NoErr(err)
	b, err := url.Parse("https://en.wikipedia.org/wiki/ZFS#cite_note-58")
	is.NoErr(err)
	is.True(urlEq(a, b))

	a, err = url.Parse("https://en.wikipedia.org/wiki/_ZFS")
	is.NoErr(err)
	b, err = url.Parse("https://en.wikipedia.org/wiki/ZFS")
	is.NoErr(err)
	is.True(!urlEq(a, b))
}
