package main

import (
	"context"
	"fmt"
	"hash"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/harrybrwn/diktyo/event"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/internal"
	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

func publish(p event.Publisher, hash hash.Hash, host string, req *web.PageRequest) error {
	hash.Reset()
	_, err := io.WriteString(hash, host)
	if err != nil {
		return err
	}
	var (
		key = fmt.Sprintf("%x.%s", hash.Sum(nil)[:2], host)
		msg = amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			Priority:     0,
		}
	)
	err = frontier.SetPageReqAsMessageBody(req, &msg)
	if err != nil {
		return err
	}
	return p.Publish(key, msg)
}

func isTooManyOpenFiles(err error) bool {
	e := internal.UnwrapAll(err)
	errno, ok := e.(syscall.Errno)
	if !ok {
		return false
	}
	return errno == syscall.EMFILE || errno == syscall.ENFILE
}

// TODO remove this
func ipAddrs(host string) (v4, v6 interface{}, err error) {
	return internal.IPAddrs(host)
}

func memoryLogs(mem *runtime.MemStats) logrus.Fields {
	lastGC := time.Since(time.Unix(0, int64(mem.LastGC)))
	return logrus.Fields{
		"heap":   fmt.Sprintf("%03.02fmb", toMB(mem.HeapAlloc)),
		"sys":    fmt.Sprintf("%03.02fmb", toMB(mem.Sys)),
		"frees":  mem.Frees,
		"GCs":    mem.NumGC,
		"lastGC": lastGC.Truncate(time.Millisecond),
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

func periodicMemLogs(ctx context.Context, d time.Duration) {
	var mem runtime.MemStats
	tick := time.NewTicker(d)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			runtime.ReadMemStats(&mem)
			log.WithFields(memoryLogs(&mem)).Info("memory")
		}
	}
}

func periodicMemDump(ctx context.Context, d time.Duration) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	err := os.Mkdir("/var/local/diktyo/mem", 0755)
	if err != nil && !os.IsExist(err) {
		log.WithError(err).Error("could not create memory dumps folder")
	}
	for {
		select {
		case <-ticker.C:
			fmt.Println("tick")
			var (
				name string
				f    *os.File
			)
			for i := 0; i < 5000; i++ {
				name = fmt.Sprintf("/var/local/diktyo/mem/dump_%d.pprof", i)
				_, err = os.Stat(name)
				if os.IsNotExist(err) {
					break
				}
			}

			f, err = os.Create(name)
			if err != nil {
				log.WithError(err).Error("could not create memory dump")
				continue
			}
			log.Infof("writing memory dump to %s", name)
			err = pprof.WriteHeapProfile(f)
			if err != nil {
				log.WithError(err).Error("could not create memory dump")
			}
			f.Close()
		case <-ctx.Done():
			return
		}
	}
}
