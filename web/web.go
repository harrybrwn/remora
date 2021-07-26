package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

var (
	ErrSkipURL = errors.New("skip url")

	log = logrus.StandardLogger()
)

type Visitor interface {
	// Filter is called after checking page depth
	// and after checking for a repeated URL.
	Filter(*PageRequest, *url.URL) error

	// Visit is called after a page is fetched.
	Visit(context.Context, *Page)

	// LinkFound is called when a new link is
	// found and popped off of the main queue
	// and before any depth checking or repeat
	// checking.
	LinkFound(*url.URL)
}

func SetLogger(l *logrus.Logger) { log = l }
func GetLogger() *logrus.Logger  { return log }

func GetRobotsTxT(host string) (*robotstxt.RobotsData, error) {
	req := &http.Request{
		Method: "GET",
		Proto:  "HTTP/1.1",
		Host:   host,
		URL: &url.URL{
			Scheme: "https",
			Host:   host,
			Path:   "/robots.txt",
		},
		Header: http.Header{
			"Pragma": {"No-Cache"},
		},
		Body:    http.NoBody,
		GetBody: defaultGetBody,
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return robotstxt.FromStatusAndBytes(resp.StatusCode, b)
}

func AllowAll() *robotstxt.RobotsData {
	r, _ := robotstxt.FromBytes(nil)
	return r
}

// RequestQueue is a queue for pages
type RequestQueue interface {
	Enqueue(*PageRequest) error
	Dequeue() (*PageRequest, error)
	Close() error
	Size() int64
}

var RetryLimit int32 = 5

func urlKey(u *url.URL) []byte {
	s := u.String()
	key := make([]byte, 8, len(s)+8)
	copy(key[:], []byte("visited_"))
	return append(key, []byte(s)...)
}

type NoOpVisitor struct{}

func (v *NoOpVisitor) Filter(*PageRequest, *url.URL) error { return nil }
func (v *NoOpVisitor) Visit(context.Context, *Page)        {}
func (v *NoOpVisitor) LinkFound(*url.URL)                  {}

type SitemapIndex struct {
	XMLName xml.Name `xml:"sitemapindex"`

	// Index is a list of sitemaps
	Index []Sitemap `xml:"sitemap"`
	// SitemapContents holds the combined contents of
	// each sitemap in the index.
	SitemapContents []SitemapURLSet `xml:"-"`
}

type Sitemap struct {
	Loc          string `xml:"loc"`
	LastModified string `xml:"lastmod"`
}

type SitemapURL struct {
	Loc          string  `xml:"loc"`
	LastModified string  `xml:"lastmod"`
	ChangeFreq   string  `xml:"changeFreq"`
	Priority     float32 `xml:"priority"`
}

type SitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLS    []SitemapURL `xml:"url"`
}

func GetSitemap(l string) (*SitemapIndex, error) {
	var buf bytes.Buffer
	req, err := http.NewRequest("GET", l, nil)
	if err != nil {
		return nil, err
	}
	resp, err := HttpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if _, err = buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if err = resp.Body.Close(); err != nil {
		return nil, err
	}

	b := buf.Bytes()
	set, err := getSitemap(b)
	if err == nil {
		return &SitemapIndex{
			Index:           []Sitemap{{Loc: l}},
			SitemapContents: []SitemapURLSet{*set},
		}, nil
	}
	log.WithError(err).Debug("got sitemap index, not one sitemap")
	return getSiteMapIndex(b)
}

func getSitemap(b []byte) (*SitemapURLSet, error) {
	m := SitemapURLSet{}
	err := xml.Unmarshal(b, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (ix *SitemapIndex) FillContents(ctx context.Context, limit int) error {
	var (
		wg   sync.WaitGroup
		ch   = make(chan *SitemapURLSet)
		next = make(chan struct{}, limit)
	)
	if len(ix.Index) == len(ix.SitemapContents) {
		return nil
	}
	ix.SitemapContents = ix.SitemapContents[:0]

	wg.Add(len(ix.Index))
	go func() {
		wg.Wait()
		close(ch)
	}()
	for _, sm := range ix.Index {
		next <- struct{}{}
		go func(s Sitemap) {
			e := requestSitemap(ctx, &s, ch, &wg)
			if e != nil {
				log.WithFields(logrus.Fields{
					"error":   e,
					"loc":     s.Loc,
					"lastmod": s.LastModified,
				}).Warn("could not get sitemap")
			}
			<-next
		}(sm)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case set, ok := <-ch:
			if !ok {
				break
			}
			ix.SitemapContents = append(ix.SitemapContents, *set)
		}
	}
}

func getSiteMapIndex(b []byte) (*SitemapIndex, error) {
	var (
		index SitemapIndex
	)
	err := xml.Unmarshal(b, &index)
	if err != nil {
		return nil, err
	}
	return &index, nil
}

func requestSitemap(ctx context.Context, sm *Sitemap, ch chan *SitemapURLSet, wg *sync.WaitGroup) error {
	defer wg.Done()
	var (
		m SitemapURLSet
		r io.Reader
	)
	log.Infof("fetching sitemap %q", sm.Loc)

	req, err := http.NewRequest("GET", sm.Loc, nil)
	if err != nil {
		return errors.Wrap(err, "could not create http request")
	}
	req = req.WithContext(ctx)
	resp, err := HttpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "could not request sitemap")
	}
	defer resp.Body.Close()

	switch resp.Header.Get("Content-Type") {
	case "application/x-gzip":
		r, err = gzip.NewReader(resp.Body)
		if err != nil {
			return errors.Wrap(err, "could not create gzip reader")
		}
	case "application/zip":
		return errors.New("cannot handle zip file")
	default:
		r = resp.Body
	}

	err = xml.NewDecoder(r).Decode(&m)
	if err != nil {
		return errors.Wrap(err, "could not decode sitemap content")
	}
	select {
	case ch <- &m:
	case <-ctx.Done():
	}
	return nil
}
