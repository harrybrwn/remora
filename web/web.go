package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/html"
)

var log = logrus.StandardLogger()

func SetLogger(l *logrus.Logger) { log = l }
func GetLogger() *logrus.Logger  { return log }

type Fetcher interface {
	Fetch(context.Context, *PageRequest) (*Page, error)
}

func NewFetcher(userAgent string, opts ...FetcherOption) *pageFetcher {
	fetcher := &pageFetcher{
		agent:  userAgent,
		client: http.Client{Timeout: time.Minute},
	}
	for _, o := range opts {
		o(fetcher)
	}
	return fetcher
}

type FetcherOption func(*pageFetcher)

func WithTransport(rt http.RoundTripper) FetcherOption {
	return func(pf *pageFetcher) { pf.client.Transport = rt }
}

func WithTimeout(d time.Duration) FetcherOption {
	return func(pf *pageFetcher) { pf.client.Timeout = d }
}

type pageFetcher struct {
	agent  string
	client http.Client
}

func (pf *pageFetcher) Fetch(ctx context.Context, req *PageRequest) (*Page, error) {
	now := time.Now()
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, err
	}
	p := NewPage(u, req.Depth)
	request := (&http.Request{
		Method:     "GET",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       u.Host,
		URL:        u,
		Body:       http.NoBody,
		GetBody:    defaultGetBody,
		Header: http.Header{
			"User-Agent": {pf.agent},
		},
	}).WithContext(ctx)
	resp, err := HttpClient.Do(request)
	if err != nil {
		return nil, err
	}
	p.ResponseTime = time.Since(now)
	p.Response = resp // TODO This will use lots of memory...
	if resp.StatusCode == http.StatusTooManyRequests {
		p.RetryAfter = getRetryTime(resp.Header)
		fields := logHeader(resp.Header)
		log.WithFields(fields).Warn(resp.Status + " " + u.String())
	}

	p.Status = resp.StatusCode
	p.ContentType = getContentType(resp)
	p.Redirected = wasRedirected(resp)
	if p.Redirected {
		p.RedirectedFrom = p.URL
		p.URL = resp.Request.URL
	}
	var (
		buf  bytes.Buffer
		hash = fnv.New128()
	)
	_, err = io.Copy(&buf, io.TeeReader(resp.Body, hash))
	if err != nil {
		return nil, errors.Wrap(err, "could not read http response body")
	}
	copy(p.Hash[:], hash.Sum(nil))
	resp.Body.Close()

	root, err := html.ParseWithOptions(&buf)
	if err != nil {
		p.IsHTML = false
		return p, nil
	}
	p.IsHTML = true
	doc := goquery.NewDocumentFromNode(root)
	p.Doc = doc
	p.Encoding = getCharset(doc)
	var e error
	p.Links, e = getLinks(doc, p.URL)
	if e != nil && err == nil {
		err = e
	}
	switch p.ContentType {
	case
		"application/zip",
		"application/x-mobipocket-ebook",
		"application/pdf",
		"application/epub+zip":
		// TODO handle these
		break
	case "text/html", "text/plain":
		p.Words, e = Keywords(doc)
		if e != nil && err == nil {
			err = e
		}
	}
	return p, err
}

// RequestQueue is a queue for pages
type RequestQueue interface {
	Enqueue(*PageRequest) error
	Dequeue() (*PageRequest, error)
	Close() error
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
func (v *NoOpVisitor) LinkFound(*url.URL) error            { return nil }

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
