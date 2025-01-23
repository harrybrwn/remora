package browser

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/harrybrwn/remora/web"
)

var (
	DefaultResourceType  = network.ResourceTypeDocument
	FlagAutoOpenDevtools = chromedp.Flag("auto-open-devtools-for-tabs", true)
)

type Fetcher struct{}

func NewFetcher() *Fetcher { return new(Fetcher) }

func (f *Fetcher) Fetch(ctx context.Context, req *web.PageRequest) (*web.Page, error) {
	baseURL, err := url.Parse(req.URL)
	if err != nil {
		return nil, err
	}
	var links []*url.URL
	tasks := chromedp.Tasks{
		chromedp.Navigate(baseURL.String()),
		WaitInteractive(),
		GetLinks(baseURL, &links),
	}
	resp, err := chromedp.RunResponse(ctx, tasks)
	if err != nil {
		return nil, err
	}
	return &web.Page{
		Status: int(resp.Status),
		URL:    baseURL,
		Links:  links,
	}, nil
}

func GetLinks(base *url.URL, links *[]*url.URL) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var nodes []*cdp.Node
		err := chromedp.Nodes("a[href], img[src]", &nodes, chromedp.ByQueryAll).Do(ctx)
		if err != nil {
			return err
		}
		ls := make([]*url.URL, 0, len(nodes))
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
			u, err := url.Parse(l)
			if err != nil {
				continue
			}
			if !u.IsAbs() {
				u = base.ResolveReference(u)
			}
			ls = append(ls, u)
		}
		*links = ls
		return nil
	})
}

func WaitPageLoaded() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		done := make(chan struct{})
		chromedp.ListenTarget(ctx, func(event any) {
			_, ok := event.(*page.EventLoadEventFired)
			if !ok {
				return
			} else {
				close(done)
			}
		})
		<-done
		return nil
	})
}

func WaitInteractive() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		done := make(chan struct{})
		chromedp.ListenTarget(ctx, func(event any) {
			ev, ok := event.(*page.EventLifecycleEvent)
			if !ok {
				return
			}
			if ev.Name == "InteractiveTime" {
				fmt.Printf("event: %[1]T %+[1]v\n", ev)
				close(done)
			}
		})
		<-done
		return nil
	})
}

// WaitForSilent will block until chrome events stop for the given duration.
func WaitForSilent(d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		chromedp.ListenTarget(ctx, func(event any) {
			switch ev := event.(type) {
			case *page.EventLifecycleEvent:
				if ev.Name == "InteractiveTime" {
					return
				}
			}
			timer.Reset(d)
		})
		<-timer.C
		return nil
	})
}

func Text(sel any, w io.Writer, opts ...chromedp.QueryOption) chromedp.QueryAction {
	return chromedp.QueryAfter(sel, func(ctx context.Context, eci runtime.ExecutionContextID, nodes ...*cdp.Node) error {
		for _, n := range nodes {
			collectText(w, n)
		}
		return nil
	}, opts...)
}

func collectText(w io.Writer, n *cdp.Node) {
	switch n.NodeType {
	case cdp.NodeTypeComment:
		return
	case cdp.NodeTypeElement:
		switch n.NodeName {
		case "SCRIPT", "STYLE", "NOSCRIPT":
			return
		}
	}
	value := strings.Trim(n.NodeValue, "\r\n\t")
	if len(value) > 0 {
		io.WriteString(w, value)
		w.Write([]byte{' '})
	}
	for _, child := range n.Children {
		collectText(w, child)
	}
}
