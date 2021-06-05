package main

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
)

func Test(t *testing.T) {
	fmt.Println("http://quotes.toscrape.com/")
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
		"http://quotes.toscrape.com/",
	}
	wg.Add(len(links))
	for _, l := range links {
		go func(l string) {
			defer wg.Done()
			u, err := url.Parse(l)
			if err != nil {
				panic(err)
			}
			p := web.NewPage(u, 0)

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

func TestRuntimeCommands(t *testing.T) {
	var (
		crawler = web.NewCrawler()
		cmd     = NewRuntimeCmd(crawler)
		buf     bytes.Buffer
	)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"count"})
	err := cmd.Execute()
	if err != nil {
		t.Error()
	}
	if buf.String() != "count: 0\n" {
		t.Error("wrong output")
	}
	buf.Reset()

	cmd.SetArgs([]string{"help"})
	err = cmd.Execute()
	if err != nil {
		t.Error(err)
	}
	if buf.Len() == 0 {
		t.Error("should output help message, got zero length output")
	}
	buf.Reset()
}
