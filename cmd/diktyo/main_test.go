package main

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
)

func Test(t *testing.T) {
}

func TestGetPage(t *testing.T) {
	page, err := pageFromHyperLink("https://en.wikipedia.org/")
	if err != nil {
		t.Fatal(err)
	}
	if page == nil {
		t.Fatal("nil page")
	}
	if len(page.Links) == 0 {
		t.Error("wikipedia front page has no links, wtf")
	}
}

func TestRedirects(t *testing.T) {
	var wg sync.WaitGroup
	links := []string{
		"https://loc.gov/help/",
		"https://creativecommons.org/legalcode",
		"http://foundation.wikimedia.org/wiki/Terms_of_Use",
	}
	wg.Add(len(links))
	for _, l := range links {
		go func(l string) {
			defer wg.Done()
			u, err := url.Parse(l)
			if err != nil {
				panic(err)
			}
			// _, err := pageFromHyperLink(l)
			p, err := newPage(u)
			if err != nil {
				t.Error(err)
				return
			}
			if p == nil {
				t.Error("nil page")
				return
			}
			// if p.doc == nil {
			// 	t.Error("nil page document")
			// }

			fmt.Println(u)
		}(l)
	}
	wg.Wait()
}
