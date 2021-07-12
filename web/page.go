package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

//go:generate protoc -I.. -I../protobuf --go_out=paths=source_relative:./pb --go-grpc_out=paths=source_relative:./pb page.proto

// go:generate protoc -I../protobuf --go_out=paths=source_relative:. ../protobuf/page_request.proto

var (
	HttpClient = &http.Client{
		Timeout: time.Minute,
	}
)

func NewPage(u *url.URL, depth uint32) *Page {
	return &Page{
		URL:   u,
		Depth: depth,
		Doc:   nil,
	}
}

func NewPageFromString(link string, depth uint32) *Page {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	return NewPage(u, depth)
}

func NewPageRequest(u *url.URL, depth uint32) *PageRequest {
	s := u.String()
	h := fnv.New128()
	io.WriteString(h, s)
	return &PageRequest{
		URL:   s,
		Depth: depth,
		Key:   h.Sum(nil),
	}
}

func ParsePageRequest(link string, depth uint32) *PageRequest {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	return NewPageRequest(u, depth)
}

func (req *PageRequest) HexKey() string {
	return hex.EncodeToString(req.Key)
}

// Page holds metadata for a webpage
type Page struct {
	URL   *url.URL
	Links []*url.URL

	Depth        uint32
	ResponseTime time.Duration

	Redirected     bool
	RedirectedFrom *url.URL
	Status         int
	ContentType    string
	RetryAfter     time.Duration

	Doc      *goquery.Document
	Title    string
	Encoding string
	Words    []string
}

// Fetch will take a page and fetch the document to retrieve
// all necessary metadata for creating a complete page struct.
func (p *Page) Fetch() error {
	return p.FetchCtx(context.Background())
}

var UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/90.0.0.0 Safari/537.36"

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
		Header:  http.Header{"User-Agent": {UserAgent}},
	}
	req = req.WithContext(ctx)
	resp, err := HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	p.ResponseTime = time.Since(now)

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

	root, err := html.ParseWithOptions(resp.Body)
	if err != nil {
		// Could be an image or non-html page,
		// don't return an error
		return nil
	}
	doc := goquery.NewDocumentFromNode(root)
	p.Doc = doc
	p.Title = getTitle(doc)
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
		break
	case "text/html", "text/plain":
		p.Words, e = Keywords(doc)
		if e != nil && err == nil {
			err = e
		}
	}
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

func Keywords(doc *goquery.Document) ([]string, error) {
	var buf bytes.Buffer
	for _, n := range doc.Nodes {
		getText(&buf, n)
	}
	s := cleanWordsRegex.ReplaceAllString(buf.String(), " ")
	return strings.Fields(s), nil
}

func (p *Page) Keywords() ([]string, error) {
	if p.Doc == nil {
		return nil, errors.New("page has no document")
	}
	return Keywords(p.Doc)
}

func getTitle(doc *goquery.Document) string {
	if doc == nil {
		return ""
	}
	ss := doc.Find("title")
	for _, n := range ss.Nodes {
		if n.DataAtom == atom.Title && n.FirstChild != nil {
			return n.FirstChild.Data
		}
	}
	return ""
}

func getCharset(doc *goquery.Document) string {
	if doc == nil {
		return ""
	}
	metas := doc.Find("meta[charset]")
	if len(metas.Nodes) == 0 {
		return ""
	}
	for _, n := range metas.Nodes {
		for _, attr := range n.Attr {
			if attr.Key == "charset" {
				return attr.Val
			}
		}
	}
	return ""
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

func getRetryTime(header http.Header) time.Duration {
	var (
		retry = header.Get("Retry-After")
		reset = header.Get("X-Ratelimit-Rest")
	)
	if reset != "" {
		t, err := strconv.ParseInt(reset, 10, 64)
		if err != nil {
			goto Retry
		}
		return time.Until(time.Unix(t, 0))
	}
Retry:
	if retry != "" {
		t, err := strconv.ParseInt(retry, 10, 64)
		if err != nil {
			return 0
		}
		return time.Second * time.Duration(t)
	}
	return 0
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
