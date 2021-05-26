package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pkg/errors"
	"golang.org/x/net/html"
)

func NewPage(u *url.URL, depth uint) *Page {
	var link url.URL = *u
	return &Page{
		URL:   &link,
		Depth: depth,
	}
}
func NewPageFromString(link string, depth uint) *Page {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	return &Page{URL: u, Depth: depth}
}

// Page holds metadata for a webpage
type Page struct {
	URL          *url.URL
	Links        []*url.URL
	Depth        uint
	contentType  string
	redirected   bool
	responseTime time.Duration
}

type PageMsg struct {
	URL   string   `json:"url"`
	Depth uint     `json:"depth"`
	Links []string `json:"links"`
}

// Fetch will take a page and fetch the document to retrieve
// all necessary metadata for creating a complete page struct.
func (p *Page) Fetch() error {
	now := time.Now()
	req := &http.Request{
		Method: "GET",
		Proto:  "HTTP/1.1",
		Host:   p.URL.Host,
		URL:    p.URL,
		Body:   http.NoBody,
		GetBody: func() (io.ReadCloser, error) {
			return http.NoBody, nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	root, err := html.ParseWithOptions(resp.Body)
	if err != nil {
		return err
	}
	p.redirected = wasRedirected(resp)
	if p.redirected {
		p.URL = resp.Request.URL
	}
	doc := goquery.NewDocumentFromNode(root)
	p.Links, err = getLinks(doc, p.URL)
	p.contentType = getContentType(resp)
	p.responseTime = time.Since(now)
	return err
}

func pageFromHyperLink(hyperlink string) (*Page, error) {
	req, err := http.NewRequest("GET", hyperlink, nil)
	if err != nil {
		return nil, err
	}
	return requestPage(req)
}

func newPage(u *url.URL) (*Page, error) {
	req := &http.Request{
		Method: "GET",
		Proto:  "HTTP/1.1",
		Host:   u.Host,
		URL:    u,
		Body:   http.NoBody,
		GetBody: func() (io.ReadCloser, error) {
			return http.NoBody, nil
		},
	}
	return requestPage(req)
}

func requestPage(req *http.Request) (*Page, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer resp.Body.Close()
	return readPage(resp.Body, resp.Request.URL)
}

func readPage(r io.Reader, from *url.URL) (*Page, error) {
	root, err := html.ParseWithOptions(r)
	if err != nil {
		return nil, err
	}
	doc := goquery.NewDocumentFromNode(root)
	links, err := getLinks(doc, from)
	if err != nil {
		return nil, err
	}
	return &Page{
		URL:   from,
		Links: links,
	}, nil
}

func getLinks(doc *goquery.Document, entry *url.URL) ([]*url.URL, error) {
	var (
		sel   = doc.Find("a[href]")
		links = make(map[string]struct{})
		urls  []*url.URL
	)
	for _, n := range sel.Nodes {
		for _, attr := range n.Attr {
			if attr.Key == "href" {
				links[attr.Val] = struct{}{}
			}
		}
	}

	urls = make([]*url.URL, 0, len(links))
	for link := range links {
		u, err := url.Parse(strings.Trim(link, "\t \n"))
		if err != nil {
			// Skip invalid hyperlinks
			continue
		}
		if u.Scheme == "" {
			u.Scheme = entry.Scheme
		}
		if u.Host == "" {
			u.Host = entry.Host
		}
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
