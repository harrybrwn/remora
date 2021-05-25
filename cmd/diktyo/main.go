package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/crypto/ssh/terminal"
)

var client = http.Client{
	Transport: http.DefaultTransport,
	Timeout:   time.Second * 20,
}

func main() {
	var (
		err  error
		args []string
		now  = time.Now()
	)
	defer func() { fmt.Println("total time:", time.Since(now)) }()

	go http.ListenAndServe(":8080", nil)

	flag := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	weight := flag.Int64("weight", 30, "set the internal semaphore weight")
	depth := flag.Uint("depth", 2, "crawl depth limit")
	flag.DurationVarP(&client.Timeout, "timeout", "t", client.Timeout, "http request timeout")
	err = flag.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		return
	}

	f, err := os.Create("urls.log")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	log.SetOutput(f)

	t := newTermCtrl()
	c := NewCollector(*depth, *weight)

	args = flag.Args()
	if len(args) < 1 {
		log.Fatal("no url")
	}

	page, err := pageFromHyperLink(args[0])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("root links:", len(page.Links))

	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)

	defer func() {
		cursorOn()
	}()

	c.sem.Acquire(context.Background(), 1)
	c.wg.Add(1)
	go c.WaitAndClose()
	go c.collect(page)
	cursorOff()

	// Wait for first level, makes it slightly
	// more like breadth first search.
	// time.Sleep(time.Second * 10)

	var (
		n      = 1
		nlinks int64
	)

	for {
		select {
		case pg, ok := <-c.pages:
			if !ok {
				goto Done // channel closed
			}
			if pg.depth > c.limit {
				break
			}

			c.sem.Acquire(context.Background(), 1)
			c.wg.Add(1)
			go c.collect(pg)

			u := pg.URL.String()
			s := fmt.Sprintf("%d (%d) %d ", n, pg.depth, c.stack)
			if len(u) > t.w {
				u = u[:t.w-len(s)-2]
			}
			log.Printf("%s%s", s, u)
			fmt.Printf("\r%s%s\x1b[0K", s, u)
			n++
			nlinks += int64(len(pg.Links))
		case err := <-c.errs:
			e := unwrapAll(err)
			if !isNoSuchHost(e) {
				fmt.Printf("\rError: %[1]v\x1b[0K\n", err)
			}
		case <-interrupts:
			close(interrupts)
			close(c.pages)
			goto Done
		}
	}
Done:
	fmt.Printf("\n%d links visited\n\x1b[0K", n)
	fmt.Printf("%f average links per page\n", float64(nlinks)/float64(n))
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

func isTooManyOpenFiles(err error) bool {
	e := unwrapAll(err)
	errno, ok := e.(syscall.Errno)
	if !ok {
		return false
	}
	return errno == syscall.EMFILE || errno == syscall.ENFILE
}

func unwrapAll(err error) error {
	var e error = err
	type unwrappable interface {
		Unwrap() error
	}
	unwrapper, ok := e.(unwrappable)
	for ok {
		e = unwrapper.Unwrap()
		unwrapper, ok = e.(unwrappable)
	}
	return e
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
	}
	return true
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
