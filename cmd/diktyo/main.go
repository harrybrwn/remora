package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"time"

	_ "github.com/lib/pq"
	"github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	client = http.Client{
		Transport: http.DefaultTransport,
		Timeout:   time.Second * 20,
	}
	log = logrus.New()
)

func main() {
	var (
		errlog = logrus.New()
		err    error
		args   []string
		now    = time.Now()
		stopAt int
	)
	defer func() { fmt.Println("total time:", time.Since(now)) }()

	go http.ListenAndServe(":8080", nil)

	flag := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	weight := flag.Int64("weight", 500, "set the internal semaphore weight")
	depth := flag.Uint("depth", 2, "crawl depth limit")
	dumpVisited := flag.String("dump-visited-set", "", "dump the visited vertex set to a file")
	flag.DurationVarP(&client.Timeout, "timeout", "t", client.Timeout, "http request timeout")
	flag.IntVar(&stopAt, "stop-at", -1, "halt the program at some number of links for pprof debugging")
	err = flag.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		return
	}

	errlog.SetFormatter(&logrus.JSONFormatter{})
	setErrorLogfile(errlog)
	setLoggerFile(log, "diktyo")

	urlsfile, err := os.Create(fmt.Sprintf("urls-depth-%d.log", *depth))
	if err != nil {
		errlog.Fatal(err)
	}
	defer urlsfile.Close()

	t := newTermCtrl()
	c := NewCollector(*depth, *weight)

	args = flag.Args()
	if len(args) < 1 {
		errlog.Fatal("no url")
	}

	page, err := pageFromHyperLink(args[0])
	if err != nil {
		errlog.Fatal(err)
	}
	fmt.Println("root links:", len(page.Links))

	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)

	defer cursorOn()

	c.sem.Acquire(context.Background(), 1)
	c.wg.Add(1)
	go c.WaitAndClose()
	go c.collect(page)
	cursorOff()

	var stats = stats{
		// root:     args[0],
		root:     page.URL.String(),
		vertices: 1,
		started:  now,
	}

	for {
		select {
		case pg, ok := <-c.pages:
			if !ok {
				goto Done // channel closed
			}
			if pg.depth > c.limit {
				continue
			}

			if stopAt > 0 && int(stats.vertices) >= stopAt {
				time.Sleep(time.Second) // slow down considerably for memory debugging
			}

			c.sem.Acquire(context.Background(), 1)
			c.wg.Add(1)
			go c.collect(pg)

			stats.collect(pg)

			c.stats.Lock()
			stack := c.stack
			files := c.files
			c.stats.Unlock()

			u := pg.URL.String()
			s := fmt.Sprintf(
				"%d depth(%d) stack(%d) files(%d) errs(%d) time(%v) ",
				stats.vertices, pg.depth,
				stack, files, stats.errors,
				pg.responseTime)
			if len(u) > t.w {
				u = u[:t.w-len(s)-10]
			}
			fmt.Fprintf(urlsfile, "%s\n", u)
			fmt.Printf("\r%s%s\x1b[0K", s, u)
		case err := <-c.errs:
			if err == nil {
				continue
			}
			stats.errors++
			e := unwrapAll(err)

			errlog.WithFields(logrus.Fields{
				"basetype":   fmt.Sprintf("%T", e),
				"nth_vertex": stats.vertices,
			}).Error(err)

			if !isNoSuchHost(e) {
				fmt.Printf("\rError: %[1]v\x1b[0K\n", err)
			} else {
				stats.deadDomains++
			}
		case <-interrupts:
			close(interrupts)
			close(c.pages)
			goto Done
		}
	}

Done:
	name, err := findNthFile("diktyo_out_%d.txt")
	if err != nil {
		errlog.Fatal(err)
	}
	out, err := os.Create(name)
	if err != nil {
		errlog.Fatal(err)
	}
	defer out.Close()
	fmt.Fprintf(out, "root url: %s\ndepth limit: %d\n", args[0], *depth)
	fmt.Printf("\n\x1b[0K") // add new line and clear terminal line
	stats.writeto(io.MultiWriter(out, os.Stdout))

	if *dumpVisited != "" {
		dumpfile, err := os.OpenFile(*dumpVisited, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			errlog.Warn("cannot dump visited set to file:", err)
			return
		}
		defer dumpfile.Close()
		err = c.writeVisitedSet(dumpfile)
		if err != nil {
			errlog.Warn("cannot dump visited set to file:", err)
		}
	}
}

func isNoSuchHost(err error) bool {
	switch v := err.(type) {
	case *url.Error:
		urlerr, ok := err.(*url.Error)
		if !ok {
			return false
		}
		operr, ok := urlerr.Err.(*net.OpError)
		if !ok {
			return false
		}
		return isNoSuchHost(operr)
	case *net.DNSError:
		return v.IsNotFound
	}
	return false
}

type stats struct {
	root string // Root URL of the web crawl

	// Number of vertices and edges seen respectively
	vertices, edges int64

	maxDegree         int    // Maximum number of links for one page
	pageWithMaxDegree string // URL of the page with the max number of links
	maxUrlLen         int    // Maximum length of all URLs visited

	errors      int // Number of errors accumulated
	deadDomains int // Number of dead domains found

	started time.Time // Time of crawl start
}

func (s *stats) writeto(w io.Writer) error {
	fmt.Fprintf(w, "%d links visited\n", s.vertices)
	fmt.Fprintf(w, "%f average links per page\n", s.averageDegree())
	fmt.Fprintf(w, "%d maximum page links %s\n", s.maxDegree, s.pageWithMaxDegree)
	fmt.Fprintf(w, "%d maximum url length\n", s.maxUrlLen)
	fmt.Fprintf(w, "%d errors collected\n", s.errors)
	fmt.Fprintf(w, "%d dead domains found\n", s.deadDomains)
	fmt.Fprintf(w, "total time: %v\n", time.Since(s.started))
	return nil
}

func (s *stats) collect(p *Page) {
	degree := len(p.Links)
	ulen := len(p.URL.String())

	if ulen > s.maxUrlLen {
		s.maxUrlLen = ulen
	}
	if degree > s.maxDegree {
		s.maxDegree = degree
		s.pageWithMaxDegree = p.URL.String()
	}

	s.vertices++
	s.edges += int64(degree)
}

func (s *stats) averageDegree() float64 {
	return float64(s.edges) / float64(s.vertices)
}

func validURLScheme(scheme string) bool {
	switch scheme {
	case
		"ftp",          // file transfer protocol
		"irc",          // IRC chat
		"mailto",       // email
		"tel",          // telephone
		"sms",          // text messaging
		"fb-messenger", // facebook messenger
		"waze",         // waze maps app
		"whatsapp",     // whatsapp messenger app
		"javascript",
		"":
		return false
	case "http", "https": // TODO add support for other protocols later
		return true
	}
	return false
}

type termCtrl struct {
	w, h int
}

func newTermCtrl() *termCtrl {
	w, h, err := terminal.GetSize(1)
	if err != nil {
		panic(err) // TODO
	}
	return &termCtrl{
		w: w, h: h,
	}
}

func cursorOn()  { fmt.Printf("\x1b[?25h") }
func cursorOff() { fmt.Printf("\x1b[?25l") }
