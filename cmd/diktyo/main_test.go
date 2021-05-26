package main

import (
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
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
	log.SetFormatter(&logrus.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.DebugLevel)
	var wg sync.WaitGroup
	links := []string{
		"https://en.wikipedia.org/",
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
			p := NewPage(u, 0)

			err = p.Fetch()
			if err != nil {
				t.Error(err)
				return
			}
			if p == nil {
				t.Error("nil page")
				return
			}
			fmt.Println(u)
			fmt.Println(p.URL)
			fmt.Println("redirected", p.URL.Path != u.Path)
			println()
		}(l)
	}
	wg.Wait()
}
