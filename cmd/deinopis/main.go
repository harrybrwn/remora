// This is one component of a distributed web crawler. Deinopis, named after the
// net-casting spider also known as the "gladiator spider", is the page fetching
// component of the whole web crawler. The deinopis is an arachnid that holds
// a web and waits to capture pray with it's web as if it were a net.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	Host       string
	Port       int
}

func (c *Config) bind(set *flag.FlagSet) {
	c.Config.Bind(set)
	set.StringVar(&c.Host, "host", c.Host, "host of website being crawled by this spider")
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

	conf.bind(flag.CommandLine)
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
	log.SetLevel(logrus.DebugLevel)

	ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	copy(args, flag.Args())
	if len(args) > 0 {
		conf.Host = args[0]
	}
	if conf.Host == "" {
		log.Fatal("no host given on startup")
		stop()
	}

	db, err := db.New(&conf.DB)
	if err != nil {
		log.WithError(err).Fatal("could not connect to database")
	}
	conn, err := amqp.Dial(conf.MessageQueue.URI())
	if err != nil {
		log.WithError(err).Fatal("could not open message queue connection")
	}
	defer conn.Close()
	crawler, err := newCrawler(conf, visitor.New(db), conn)
	if err != nil {
		log.WithError(err).Fatal("could not create crawler")
		stop()
	}
	defer crawler.Close()
	crawler.Prefetch = conf.MessageQueue.Prefetch

	fmt.Println(crawler.Prefetch)
	go func() {
		<-ctx.Done()
		fmt.Println("context canceled")
	}()

	err = crawler.listen(ctx)
	if err != nil {
		log.WithError(err).Error("listen failed")
		stop()
	}
}

func getWait(host string, conf *cmd.Config) time.Duration {
	var tm time.Duration
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
