package main

import (
	"context"
	"database/sql"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/db"
	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal"
	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
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

func getDataStores(conf *cmd.Config) (d *sql.DB, r *redis.Client, err error) {
	d, err = db.New(&conf.DB)
	if err != nil {
		err = errors.Wrap(err, "could not connect to sql database")
		return
	}
	r = redis.NewClient(conf.RedisOpts())
	if err = r.Ping().Err(); err != nil {
		d.Close()
		err = errors.Wrap(err, "could not ping redis server")
		return
	}
	return
}

func purgeRedis(conf *cmd.Config, args []string) error {
	opts := conf.RedisOpts()
	opts.WriteTimeout = time.Minute
	c := redis.NewClient(opts)
	if err := c.Ping().Err(); err != nil {
		return err
	}
	if len(args) == 0 {
		return c.FlushDB().Err()
	}
	return c.Del(args...).Err()
}

func gatherSitemap(ctx context.Context, host string, ch chan string, wg *sync.WaitGroup) error {
	defer wg.Done()
	robs, err := web.GetRobotsTxT(host)
	if err != nil {
		log.WithError(err).Warn("could not get robots.txt")
		return err
	}
	for _, sitemapURL := range robs.Sitemaps {
		wg.Add(1)
		go func(l string) {
			defer wg.Done()
			sitemapindex, err := web.GetSitemap(l)
			if err != nil {
				log.WithError(err).WithField("url", l).Warn("could not get sitemap")
				return
			}
			sitemapindex.FillContents(ctx, 3)
			for _, sitemap := range sitemapindex.SitemapContents {
				for _, u := range sitemap.URLS {
					select {
					case ch <- u.Loc:
					case <-ctx.Done():
						return
					}
				}
			}
		}(sitemapURL)
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sendHead(url string) error {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	var l int
	for key := range resp.Header {
		l = max(l, len(key))
	}
	for key, vals := range resp.Header {
		fmt.Printf("%s%s%v\n", key, strings.Repeat(" ", l-len(key)+1), vals)
	}
	fmt.Println()
	s := resp.Header.Get("Last-Modified")
	if s != "" {
		lastMod, err := time.Parse(time.RFC1123, s)
		if err != nil {
			return err
		}
		fmt.Println("last modified", time.Since(lastMod), "ago")
	}
	s = resp.Header.Get("Cache-Control")
	if s != "" {
		fmt.Println("Cache Control:", s)
	}
	return nil
}

func getWait(
	host string,
	conf *cmd.Config,
	cmd *cobra.Command,
) time.Duration {
	var tm time.Duration
	f := cmd.Flags().Lookup("sleep")
	if f.Changed {
		return conf.Sleep
	}
	t, ok := conf.WaitTimes[host]
	if ok {
		var err error
		tm, err = time.ParseDuration(t)
		if err != nil {
			tm = conf.Sleep
		}
	} else {
		tm = conf.Sleep
	}
	if tm == 0 {
		tm = 1
	}
	return tm
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
		// "frees":  mem.Frees,
		// "GCs":    mem.NumGC,
		"heap":   fmt.Sprintf("%03.02fmb", toMB(mem.HeapAlloc)),
		"lastGC": lastGC.Truncate(time.Millisecond),
		"sys":    fmt.Sprintf("%03.02fmb", toMB(mem.Sys)),
	}
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
	const base = "/var/local/remora/mem"
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	err := os.Mkdir(base, 0755)
	if err != nil && !os.IsExist(err) {
		log.WithError(err).Error("could not create memory dumps folder")
	}
	for {
		select {
		case <-ticker.C:
			var (
				name string
				f    *os.File
			)
			for i := 0; i < 5000; i++ {
				name = filepath.Join(base, fmt.Sprintf("dump_%d.pprof", i))
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
