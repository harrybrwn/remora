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

func Test(t *testing.T) {
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
	logger := logrus.New()
	logger.Level = logrus.WarnLevel
	bus := event.NewChannelBus()
	consumer, err := bus.Consumer("e4e4.en.wikipedia.org")
	is.NoErr(err)
	eventPub, err := bus.Publisher()
	is.NoErr(err)
	urlset := storage.NewInMemoryURLSet()
	publisher := &PagePublisher{
		URLSet:    urlset,
		Publisher: eventPub,
		Robots:    &allowAllRobotsCtrl{},
		Logger:    logger,
	}
	c := Crawler{
		Host:       "en.wikipedia.org",
		URLSet:     urlset,
		Fetcher:    &mockFetcher{Page: page},
		RobotsCtrl: &allowAllRobotsCtrl{},
		Consumer:   consumer,
		Publisher:  publisher,
		Visitor:    &logVisitor{logger.WithField("visitor", "*logVisitor")},
		Wait:       time.Millisecond,
		DepthLimit: 3,
		Logger:     logger,
	}
	done := make(chan struct{})
	go func() {
		is.NoErr(publisher.Publish(context.Background(), 0, []*url.URL{page.URL}))
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
	err = c.handle(ctx, request)
	is.NoErr(err)
	is.True(c.URLSet.Has(ctx, page.URL))
	is.NoErr(c.Stop())
	is.True(!c.Running())
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
