package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/pkg/errors"
	"golang.org/x/net/html"
)

type Page struct {
	URL         *url.URL
	Links       []*url.URL
	doc         *goquery.Document
	depth       uint
	contentType string
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

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		fmt.Println("\n\r", resp.Status, req.URL)
		// panic(resp.Status)
	}
	defer resp.Body.Close()
	root, err := html.ParseWithOptions(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "could not parse request")
	}

	doc := goquery.NewDocumentFromNode(root)
	links, err := getLinks(doc, req.URL)
	if err != nil {
		return nil, errors.Wrap(err, "could not collect links")
	}
	return &Page{
		URL:         req.URL,
		Links:       links,
		doc:         doc,
		contentType: resp.Header.Get("Content-Type"),
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
