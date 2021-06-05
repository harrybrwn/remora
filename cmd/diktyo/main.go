package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/db"
	"github.com/harrybrwn/diktyo/internal/logging"
	"github.com/harrybrwn/diktyo/web"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	client = http.Client{
		Transport: http.DefaultTransport,
		Timeout:   time.Second * 20,
	}
	log     = logrus.New()
	logfile = lumberjack.Logger{
		Filename:   "crawler.log",
		MaxSize:    500,
		MaxBackups: 25,
		MaxAge:     355,
		Compress:   false,
	}
)

type Config struct {
	DB           db.Config     `yaml:"db" config:"db"`
	QueueSize    int64         `yaml:"queue_size" default:"500"`
	Seeds        []string      `yaml:"seeds"`
	Depth        uint          `yaml:"depth" config:"depth" default:"2"`
	AllowedHosts []string      `yaml:"allowed_hosts" config:"allowed_hosts"`
	Sleep        time.Duration `yaml:"sleep" default:"250ms"`
}

func (c *Config) bind(flag *flag.FlagSet) {
	flag.Int64Var(&c.QueueSize, "queue", c.QueueSize, "Size of the main queue")
	flag.StringArrayVarP(&c.AllowedHosts, "allowed-hosts", "a", c.AllowedHosts,
		"A list of hosts that the crawler is allowed to "+
			"visit. The host of seed urls are added implicitly")
	flag.DurationVar(&c.Sleep, "sleep", c.Sleep, "Sleep time for each spider request")
	flag.UintVar(&c.Depth, "depth", c.Depth, "Crawler depth limit")
}

func main() {
	var conf Config
	godotenv.Load()
	initLogger()
	initConfig(&conf)

	cmd := NewCLIRoot(&conf)
	err := cmd.Execute()
	if err != nil {
		fmt.Println(err)
	}
}

func NewCLIRoot(conf *Config) *cobra.Command {
	c := &cobra.Command{
		Use:   "diktyo",
		Short: "Web crawler",
	}
	crawlCmd := &cobra.Command{
		Use:   "crawl",
		Short: "Start a full web crawl",
		Long:  "Start a full web crawl.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				conf.Seeds = args
			}
			return crawl(cmd.Context(), conf)
		},
	}
	conf.bind(crawlCmd.Flags())

	c.AddCommand(
		crawlCmd,
		config.NewConfigCommand(),
		&cobra.Command{
			Use:   "list <url>",
			Short: "List the urls on a page",
			Args:  cobra.MinimumNArgs(1),
			RunE:  runListCmd,
		},
		&cobra.Command{
			Use:  "keywords",
			Args: cobra.MinimumNArgs(1),
			RunE: runKeywordsCmd,
		},
		&cobra.Command{
			Use: "test", Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return nil
			},
		},
	)
	c.SetUsageTemplate(config.IndentedCobraHelpTemplate)
	return c
}

func crawl(ctx context.Context, conf *Config) error {
	var (
		err   error
		start = time.Now()
		sigs  = make(chan os.Signal, 1)
	)
	signal.Notify(sigs, os.Interrupt)
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	db, err := db.New(&conf.DB)
	if err != nil {
		return errors.Wrap(err, "could not connect to database")
	}
	defer db.Close()

	var (
		visitor = visitor{db: db, hosts: make(map[string]struct{})}
		ch      = make(chan *web.Page, conf.QueueSize)
		crawler = web.NewCrawler(
			web.WithVisitor(&visitor),
			web.WithLimit(config.GetUint("depth")),
			web.WithQueue(ch),
		)
	)
	crawler.Sleep = conf.Sleep

	crawler.DB, err = badger.Open(badger.DefaultOptions("./_queue"))
	if err != nil {
		panic(err)
	}
	defer func() {
		crawler.DB.Close()
		os.RemoveAll("./_queue")
	}()

	if len(conf.Seeds) < 1 {
		return errors.New("no seed URLs")
	}
	visitor.addHosts(conf.AllowedHosts)

	go runtimeCommandHandler(ctx, stop, crawler)

	for _, seed := range conf.Seeds {
		u, err := url.Parse(seed)
		if err != nil {
			log.Error(err)
			continue
		}
		visitor.hosts[u.Host] = struct{}{}
		crawler.Enqueue(web.NewPage(u, 0))
	}

	log.WithFields(logrus.Fields{
		"seeds": conf.Seeds, "max_depth": config.GetUint("depth"),
	}).Info("Starting Crawl")

	crawler.Add(1)
	go crawler.Crawl(ctx)
	go http.ListenAndServe(":8080", nil)

	select {
	case <-sigs:
		stop()
		close(ch)
		fmt.Println()
	case <-ctx.Done():
	}
	log.WithFields(logrus.Fields{
		"duration": time.Since(start),
		"total":    crawler.N(),
	}).Info("Stopping Crawler")
	return nil
}

func initConfig(c *Config) {
	config.SetFilename("diktyo.yml")
	config.AddPath(".")
	config.SetType("yaml")
	config.SetConfig(c)
	err := config.InitDefaults()
	if err != nil {
		log.Fatal(errors.Wrap(err, "could not set config defaults"))
	}
	if err := config.ReadConfigFile(); err != nil {
		log.Fatal(errors.Wrap(err, "could not read config"))
	}
}

func initLogger() {
	// log.AddHook(logging.NewLogFileHook(&logfile, &logrus.JSONFormatter{
	// 	TimestampFormat:   time.RFC3339,
	// 	DisableHTMLEscape: true,
	// }))
	log.AddHook(logging.NewLogFileHook(&logfile, &logrus.TextFormatter{
		DisableColors:   true,
		PadLevelText:    true,
		TimestampFormat: time.RFC3339,
	}))
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.TraceLevel)
	log.SetFormatter(&logging.PrefixedFormatter{
		Prefix:           "",
		TimeFormat:       time.RFC3339,
		MaxMessageLength: 135,
	})
	web.SetLogger(log)
}
