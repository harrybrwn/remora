package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/harrybrwn/remora/web/browser"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := logrus.StandardLogger()
	logger.Level = logrus.InfoLevel
	logger.Out = os.Stdout
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), append(
		chromedp.DefaultExecAllocatorOptions[:],
		// []chromedp.ExecAllocatorOption{},
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoFirstRun,
		// chromedp.Flag("headless", false),
		// browser.FlagAutoOpenDevtools,
	)...)
	defer cancel()
	ctx, cancel = chromedp.NewContext(
		ctx,
		chromedp.WithBrowserOption(
			chromedp.WithConsolef(logger.Infof),
			chromedp.WithBrowserLogf(logger.Infof),
			chromedp.WithBrowserDebugf(logger.Debugf),
			chromedp.WithBrowserErrorf(logger.Errorf),
		),
	)
	defer cancel()

	var wg sync.WaitGroup

	for _, t := range []string{
		// "http://jumpcloud.com",
		"http://en.wikipedia.org/wiki/Main_Page",
		// "https://www.goodreads.com/help?action_type=help_web_footer",
	} {
		wg.Add(1)
		go run(ctx, t, &wg)
	}
	wg.Wait()

	// page, err := web.NewFetcher("").Fetch(ctx, web.NewPageRequest(baseURL, 0))
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// fmt.Printf("Go found %d links\n", len(page.Links))
}

func run(ctx context.Context, target string, wg *sync.WaitGroup) {
	ctx, cancel := chromedp.NewContext(ctx)
	defer cancel()
	defer wg.Done()
	baseURL, err := url.Parse(target)
	if err != nil {
		log.Fatal(err)
	}
	var (
		title string
		links []*url.URL
		text  bytes.Buffer
	)
	tasks := chromedp.Tasks{
		network.Enable(),
		page.Enable(),
		page.SetLifecycleEventsEnabled(true), // enable lifecycle events
		browser.Navigate(baseURL),
		browser.WaitInteractive(),
		browser.GetLinks(baseURL, &links),
		chromedp.Title(&title),
		browser.Text("html", &text),

		// chromedp.ActionFunc(func(ctx context.Context) error {
		// 	root, err := dom.GetDocument().Do(ctx)
		// 	if err != nil {
		// 		return err
		// 	}
		// 	fmt.Printf("%#v\n", root)
		// 	return nil
		// }),
	}
	res, err := browser.RunRequest(ctx, tasks...)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Browser Found %d links from %q\n", len(links), target)
	for r := res.Response; r != nil; r = r.Request.Response {
		fmt.Println(r.Status, r.URL)
	}
	fmt.Println(title, text.Len())
}
