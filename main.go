package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sync/semaphore"
)

var client = http.Client{
	Transport: http.DefaultTransport,
	Timeout:   time.Second * 10,
}

func main() {
	go http.ListenAndServe(":8080", nil)

	var (
		args []string
		lim  uint = 1
	)

	flag.UintVar(&lim, "depth", lim, "craw depth limit")
	flag.Parse()

	t := newTermCtrl()
	c := NewCollector(lim)

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

	n := 1
	defer func() {
		r := recover()
		if r != nil {
			fmt.Println(r)
		}
		cursorOn()
	}()

	c.wg.Add(1)
	go c.WaitAndClose()
	go c.collect(page)
	cursorOff()

	for {
		select {
		case pg, ok := <-c.pages:
			if !ok {
				return // channel closed
			}

			if pg.depth > lim {
				break
			}

			c.wg.Add(1)
			go c.collect(pg)

			u := pg.URL.String()
			s := fmt.Sprintf("%d (%d) %d ", n, pg.depth, c.stack)
			if len(u) > t.w {
				u = u[:t.w-len(s)-1]
			}
			fmt.Printf("\r%s%s\x1b[0K", s, u)
			n++
		case <-interrupts:
			fmt.Printf("\n%d links visited\n\x1b[0K", n)
			close(interrupts)
			close(c.pages)
			return
		}
	}
}

func NewCollector(crawLimit uint) *pageCollector {
	const size = 2000
	return &pageCollector{
		limit:   crawLimit,
		pages:   make(chan *Page, size),
		errs:    make(chan error, size),
		blocker: make(chan struct{}, 20),
		// sem: semaphore.NewWeighted(50),
		// sem: semaphore.NewWeighted(5),
		set: make(map[string]struct{}),
	}
}

type pageCollector struct {
	wg sync.WaitGroup

	pages   chan *Page
	errs    chan error
	blocker chan struct{}
	sem     *semaphore.Weighted
	limit   uint

	mu  sync.Mutex
	set map[string]struct{}

	stack     int
	count     int
	openFiles int
}

func (pc *pageCollector) WaitAndClose() {
	pc.wg.Wait()
	close(pc.pages)
}

func (pc *pageCollector) collect(page *Page) error {
	pc.mu.Lock()
	// fmt.Println("start collect", pc.stack, pc.count, page.URL, len(pc.pages))
	pc.stack++
	pc.count++
	pc.mu.Unlock()
	defer func() {
		pc.mu.Lock()
		pc.stack--
		// fmt.Println("end   collect", pc.stack, pc.count)
		pc.mu.Unlock()
	}()

	var (
		err error
		p   *Page
		// ctx = context.Background()
	)
	defer pc.wg.Done()

	if pc.tryMarkVisited(page.URL) {
		return nil
	}
	if page.depth > pc.limit {
		return nil
	}

	for _, l := range page.Links {
		if pc.visited(*l) {
			continue
		}

		if !validURLScheme(l.Scheme) {
			continue
		}
		if len(l.Path) >= 2 && l.Path[0:2] == "./" {
			continue
		}

		pc.wg.Add(1)
		// if err = pc.sem.Acquire(context.Background(), 1); err != nil {
		// 	log.Println("semaphore acquire failed:", err)
		// 	return err
		// 	// return
		// }
		pc.blocker <- struct{}{}
		go func(l *url.URL) {
			pc.mu.Lock()
			pc.openFiles++
			// fmt.Println(pc.openFiles, l)
			pc.mu.Unlock()
			defer func() {
				pc.mu.Lock()
				pc.openFiles--
				pc.mu.Unlock()
			}()
			defer pc.wg.Done()

			p, err = newPage(l)
			if err != nil {
				e := unwrapAll(err)
				if !isNoSuchHost(e) {
					fmt.Printf("\rError: %[1]T %[1]v\n\x1b[0K", e)
				}
				if errno, ok := e.(syscall.Errno); ok {
					if errno == syscall.ENFILE || errno == syscall.EMFILE {
						// panic("no more open files") // TODO log as level ERROR
						fmt.Printf("\rError: no more file descriptors: %d\n\x1b[0K", pc.openFiles)
					}
				}
				goto Next
			}
			p.depth = page.depth + 1
			pc.pages <- p
			// go func(p *Page) { pc.pages <- p }(p)
		Next:
			// pc.sem.Release(1)
			<-pc.blocker
		}(l)
	}
	return nil
}

func (pc *pageCollector) visited(u url.URL) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if u.Path == "" && u.Fragment != "" {
		u.Fragment = ""
	}
	_, ok := pc.set[u.String()]
	return ok
}

func (pc *pageCollector) tryMarkVisited(u *url.URL) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	var link url.URL = *u
	if link.Path == "" && link.Fragment != "" {
		link.Fragment = ""
	}
	_, ok := pc.set[link.String()]

	if ok {
		return true
	}
	pc.set[link.String()] = struct{}{}
	return false
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
	return false
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
	case "ftp": // file transfer protocol
		fallthrough
	case "irc":
		fallthrough
	case "mailto":
		fallthrough
	case "tel": // telephone
		fallthrough
	case "sms": // text messaging
		fallthrough
	case "fb-messenger": // facebook messenger
		fallthrough
	case "waze": // waze maps application
		fallthrough
	case "whatsapp":
		fallthrough
	case "javascript":
		fallthrough
	case "":
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
