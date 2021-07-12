// This is one component of a distributed web crawler. Deinopis, named after the
// net-casting spider also known as the "gladiator spider", is the page fetching
// component of the whole web crawler. The deinopis is an arachnid that holds
// a web and waits to capture pray with it's web as if it were a net.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
	"github.com/harrybrwn/diktyo/internal/visitor"
	"github.com/harrybrwn/diktyo/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"github.com/streadway/amqp"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	cmd.Config `yaml:",inline"`
	Host       []string
	Port       int
}

func (c *Config) bind(set *flag.FlagSet) {
	c.Config.Bind(set)
	set.StringArrayVar(&c.Host, "host", c.Host, "host of website being crawled by this spider")
	set.IntVarP(&c.Port, "port", "p", c.Port, "port of grpc server")
	set.IntVar(&c.MessageQueue.Prefetch, "prefetch", c.MessageQueue.Prefetch, "message queue prefetch limit")
}

var (
	log     = logrus.StandardLogger()
	logfile = lumberjack.Logger{
		Filename:   "deinopis.log",
		MaxSize:    500,
		MaxBackups: 25,
		MaxAge:     355,
		Compress:   false,
	}
)

func main() {
	var (
		args []string
		stop context.CancelFunc
		ctx  context.Context
		conf = &Config{}
	)
	setup(conf)
	ctx, stop = signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	copy(args, flag.Args())
	if len(args) > 0 {
		conf.Host = args
	}
	if len(conf.Host) == 0 {
		stop()
		log.Fatal("no hosts given on startup")
	}

	db, conn, redis, err := getDataStores(conf)
	if err != nil {
		log.WithError(err).Fatal("datastore connection failure")
	}
	defer func() {
		db.Close()
		conn.Close()
		redis.Close()
	}()

	visitor := visitor.New(db)
	crawlers := make([]*crawler, len(conf.Host))
	for i, host := range conf.Host {
		crawlers[i], err = newCrawler(host, visitor, conn, redis, conf)
		if err != nil {
			stop()
			log.WithError(err).Fatal("could not create crawler")
		}
	}
	defer func() {
		for _, c := range crawlers {
			c.Close()
		}
	}()

	var wg sync.WaitGroup
	start := func(c *crawler) {
		defer wg.Done()
		err = c.listen(ctx)
		if err != nil {
			stop()
			log.WithError(err).Error("listen failed")
			return
		}
	}
	wg.Add(len(crawlers))
	for _, crawler := range crawlers {
		go start(crawler)
	}
	go periodicMemoryLogging(ctx, time.Second*10)
	go func() {
		wg.Wait()
		stop()
	}()
	<-ctx.Done()
}

func getDataStores(conf *Config) (d *sql.DB, c *amqp.Connection, r *redis.Client, err error) {
	d, err = db.New(&conf.DB)
	if err != nil {
		err = errors.Wrap(err, "could not connect to sql database")
		return
	}
	c, err = amqp.Dial(conf.MessageQueue.URI())
	if err != nil {
		d.Close()
		err = errors.Wrap(err, "could not dial message queue")
		return
	}
	r = redis.NewClient(conf.RedisOpts())
	if err = r.Ping().Err(); err != nil {
		d.Close()
		c.Close()
		err = errors.Wrap(err, "could not ping redis server")
		return
	}
	return
}

func setup(conf *Config) {
	conf.bind(flag.CommandLine)
	flag.BoolVarP(&visitor.Verbose, "verbose", "v", visitor.Verbose, "set verbosity")
	config.SetConfig(conf)
	config.AddFile("config.yml")
	config.SetType("yaml")
	config.AddPath("/var/local/diktyo")
	config.AddPath(".")
	config.InitDefaults()
	err := config.ReadConfig()
	if err == config.ErrNoConfigFile {
		log.Fatal(errors.Wrap(err, "could not find config file"))
	}
	flag.Parse()
	visitor.SetLogger(log)
	web.SetLogger(log)
	if visitor.Verbose {
		log.SetLevel(logrus.TraceLevel)
	} else {
		log.SetLevel(logrus.DebugLevel)
	}
	log.SetLevel(logrus.DebugLevel)
	// log.SetFormatter(logging.NewPrefixedFormatter("deinopis", time.RFC3339))
}

func getWait(host string, conf *cmd.Config) time.Duration {
	var tm time.Duration
	if f := flag.Lookup("sleep"); f.Changed {
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

func redisClient(opts *redis.Options) (*redis.Client, error) {
	c := redis.NewClient(opts)
	return c, c.Ping().Err()
}

func periodicMemoryLogging(ctx context.Context, t time.Duration) {
	tick := time.NewTicker(t)
	defer tick.Stop()
	var mem runtime.MemStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			runtime.ReadMemStats(&mem)
			lastGC := time.Since(time.Unix(0, int64(mem.LastGC)))
			log.WithFields(logrus.Fields{
				"hostname": hostname,
				"heap":     fmt.Sprintf("%03.02fmb", toMB(mem.HeapAlloc)),
				"sys":      fmt.Sprintf("%03.02fmb", toMB(mem.Sys)),
				"frees":    mem.Frees,
				"GCs":      mem.NumGC,
				"lastGC":   lastGC.Truncate(time.Millisecond),
			}).Info("mem stats")
		}
	}
}

func toMB(bytes uint64) float64 { return float64(bytes) / 1024.0 / 1024.0 }
