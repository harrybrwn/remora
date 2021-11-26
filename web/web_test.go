package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func TestHeadlessFetcher(t *testing.T) {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	var nodes []*cdp.Node
	err := chromedp.Run(ctx,
		chromedp.Navigate("https://www.facebook.com/"),
		chromedp.Nodes("a[href], img[src]", &nodes),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		var l string
		switch strings.ToUpper(n.NodeName) {
		case "IMG":
			src, ok := n.Attribute("src")
			if ok {
				l = src
			}
		case "A":
			href, ok := n.Attribute("href")
			if ok {
				l = href
			}
		}
		if l[0] == '#' {
			continue
		}
		fmt.Println(l)
	}
}
