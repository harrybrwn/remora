package web

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var HttpClient = http.DefaultClient

func NewPage(u *url.URL, depth uint) *Page {
	return &Page{
		URL:   u,
		Depth: depth,
		Doc:   nil,
	}
}

func NewPageFromString(link string, depth uint) *Page {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	return NewPage(u, depth)
}

// Page holds metadata for a webpage
type Page struct {
	URL   *url.URL
	Links []*url.URL

	Depth          uint
	ResponseTime   time.Duration
	Redirected     bool
	RedirectedFrom *url.URL
	Status         int
	ContentType    string
	Doc            *goquery.Document
}

// Fetch will take a page and fetch the document to retrieve
// all necessary metadata for creating a complete page struct.
func (p *Page) Fetch() error {
	return p.FetchCtx(context.Background())
}

// FetchCtx will take a page and fetch the document to retrieve
// all necessary metadata for creating a complete page struct.
func (p *Page) FetchCtx(ctx context.Context) error {
	now := time.Now()
	u := p.URL
	req := &http.Request{
		Method:  "GET",
		Proto:   "HTTP/1.1",
		Host:    p.URL.Host,
		URL:     u,
		Body:    http.NoBody,
		GetBody: defaultGetBody,
		Header: http.Header{
			"User-Agent": {time.Now().String()},
		},
	}
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	p.ResponseTime = time.Since(now)

	if resp.StatusCode == http.StatusTooManyRequests {
		log.WithFields(logHeader(resp.Header)).Warn(resp.Status)
	}
	p.Status = resp.StatusCode
	p.ContentType = getContentType(resp)
	p.Redirected = wasRedirected(resp)
	if p.Redirected {
		p.RedirectedFrom = p.URL
		p.URL = resp.Request.URL
	}

	root, err := html.ParseWithOptions(resp.Body)
	if err != nil {
		return err
	}
	doc := goquery.NewDocumentFromNode(root)
	p.Doc = doc
	p.Links, err = getLinks(doc, p.URL)
	return err
}

func (p *Page) Head(ctx context.Context) error {
	u := p.URL
	req := &http.Request{
		Method:  "HEAD",
		Proto:   "HTTP/1.1",
		URL:     u,
		Body:    http.NoBody,
		GetBody: defaultGetBody,
	}
	req = req.WithContext(ctx)
	resp, err := HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

var cleanWordsRegex = regexp.MustCompile(`[\(\)/.,!?;:'"\[\]]`)

func Keywords(doc *goquery.Document) ([][]byte, error) {
	var (
		buf   bytes.Buffer
		words = make([][]byte, 0, 100)
		pos   = 0
	)
	for _, n := range doc.Nodes {
		getText(&buf, n)
	}

	var (
		b = cleanWordsRegex.ReplaceAll(buf.Bytes(), []byte{' '})
		l = buf.Len()
	)
	for {
		adv, tok, err := bufio.ScanWords(b, false)
		if err != nil {
			return nil, err
		}
		if pos > l || tok == nil {
			break
		}
		words = append(words, tok)
		b = b[adv:]
		pos += adv
	}
	return words, nil
}

func (p *Page) Keywords() ([][]byte, error) {
	if p.Doc == nil {
		return nil, errors.New("page has no document")
	}
	return Keywords(p.Doc)
}

func getLinks(doc *goquery.Document, entry *url.URL) ([]*url.URL, error) {
	var (
		sel   = doc.Find("a[href], img[src]")
		links = make(map[string]struct{})
		urls  []*url.URL
	)
	for _, n := range sel.Nodes {
		for _, attr := range n.Attr {
			if attr.Key == "href" {
				links[attr.Val] = struct{}{}
			}
			if attr.Key == "src" && !strings.HasPrefix(attr.Val, "data:image/") {
				links[attr.Val] = struct{}{}
			}
		}
	}

	urls = make([]*url.URL, 0, len(links))
	for link := range links {
		u, err := url.Parse(link)
		if err != nil {
			// Skip invalid hyperlinks
			continue
		}
		// Will only resolve a relative url and will
		// ignore urls that are not relative.
		u = entry.ResolveReference(u)
		urls = append(urls, u)
	}
	return urls, nil
}

func wasRedirected(resp *http.Response) bool {
	for resp != nil {
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			return true
		}
		if resp.Request == nil {
			break
		}
		resp = resp.Request.Response
	}
	return false
}

func getContentType(resp *http.Response) string {
	ct := resp.Header.Get("Content-Type")
	parts := strings.Split(ct, ";")
	if len(parts) < 1 {
		return ct
	}
	return parts[0]
}

func defaultGetBody() (io.ReadCloser, error) { return http.NoBody, nil }

func logHeader(h http.Header) logrus.Fields {
	f := make(logrus.Fields, len(h))
	for key, list := range h {
		f[key] = list
	}
	return f
}

func getText(buf *bytes.Buffer, n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Script, atom.Style:
			// skip elements that have text inside
			// but are not rendered
			return
		}
	}

	if n.Type == html.TextNode {
		buf.WriteString(n.Data)
	}
	if n.FirstChild == nil {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		getText(buf, c)
	}
}
