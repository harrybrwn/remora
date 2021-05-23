package main

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
)

func _Test(t *testing.T) {
	var (
		all = make([]*url.URL, 0)
		mu  sync.Mutex
		wg  sync.WaitGroup
	)
	page, err := pageFromHyperLink("https://en.wikipedia.org/wiki/Main_Page")
	if err != nil {
		t.Fatal(err)
	}

	for _, l := range page.Links {
		all = append(all, l)
		wg.Add(1)
		go func(l *url.URL) {
			defer wg.Done()
			mu.Lock()
			p, err := newPage(l)
			if err != nil {
				t.Error(err)
			}
			fmt.Println(l)
			all = append(all, p.Links...)
			mu.Unlock()
		}(l)
	}
	wg.Wait()

	for _, u := range all {
		fmt.Println(u)
	}
	fmt.Println(len(all))
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
	for _, l := range []string{
		"https://loc.gov/help/",
		"https://creativecommons.org/legalcode",
		"http://foundation.wikimedia.org/wiki/Terms_of_Use",
	} {
		wg.Add(1)
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
			if p.doc == nil {
				t.Error("nil page document")
			}

			fmt.Println(u)
		}(l)
	}
	wg.Wait()
}
