package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/go-redis/redis"
	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/internal/logging"
	"github.com/harrybrwn/diktyo/internal/visitor"
	"github.com/harrybrwn/diktyo/storage"
	"github.com/harrybrwn/diktyo/web"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
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

func main() {
	var (
		err  error
		conf cmd.Config
	)
	godotenv.Load()
	initConfig(&conf)
	lvl, err := logrus.ParseLevel(conf.LogLevel)
	if err != nil {
		log.Warn("could not parse log level")
		lvl = logrus.DebugLevel
	}
	initLogger(lvl)
	web.RetryLimit = 0

	cmd := NewCLIRoot(&conf)
	conf.Bind(cmd.PersistentFlags())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = cmd.ExecuteContext(ctx)
	if err != nil {
		fmt.Println(err)
	}
}

func NewCLIRoot(conf *cmd.Config) *cobra.Command {
	c := &cobra.Command{
		Use:   "remora",
		Short: "The internet's symbiotic web crawler",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			lvl, err := logrus.ParseLevel(conf.LogLevel)
			if err != nil {
				log.WithError(err).Warn("could not parse log level")
				return nil
			}
			log.SetLevel(lvl)
			return nil
		},
	}
	c.PersistentFlags().StringVar(&conf.LogLevel, "loglevel", conf.LogLevel, "set log level")
	// c.PersistentFlags().UintVar(&conf.Depth, "depth", conf.Depth, "set the crawl depth limit (0 for no limit)")
	// conf.Bind(c.PersistentFlags())

	crawlFlags := crawlFlags{}
	crawlCmd := &cobra.Command{
		Use:   "crawl",
		Short: "Start a full web crawl",
		Long:  "Start a full web crawl.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				conf.Seeds = args
			}
			ctx := cmd.Context()
			// http://localhost:8080/debug/pprof
			go http.ListenAndServe(":8080", nil)
			go periodicMemDump(ctx, time.Minute*10)
			return crawl(ctx, conf, &crawlFlags)
		},
	}
	crawlCmd.Flags().BoolVar(&crawlFlags.redis, "redis", crawlFlags.redis, "use redis as the visited url set")

	c.AddCommand(
		newSpiderCmd(conf),
		config.NewConfigCommand(),
		&cobra.Command{
			Use:   "list <url>",
			Short: "List the urls on a page",
			Args:  cobra.MinimumNArgs(1),
			RunE:  runListCmd,
		},
		&cobra.Command{
			Use:   "keywords",
			Short: "Print the keywords for a website",
			Args:  cobra.MinimumNArgs(1),
			RunE:  runKeywordsCmd,
		},
		newPurgeCmd(conf),
		newEnqueueCmd(conf),
		crawlCmd,
		newQueueCmd(conf),
		newRedisCmd(conf),
		&cobra.Command{
			Use: "test", Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				for i := 0; i < 30; i++ {
					log.Info(i)
				}
				return nil
			},
		},
	)
	c.SetUsageTemplate(config.IndentedCobraHelpTemplate)
	return c
}

type crawlFlags struct {
	redis bool
}

func crawl(ctx context.Context, conf *cmd.Config, flags *crawlFlags) error {
	var (
		err   error
		start = time.Now()
		sigs  = make(chan os.Signal, 1)
	)
	signal.Notify(sigs, os.Interrupt)
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	var qdb *badger.DB
	opts := badger.DefaultOptions("/var/local/diktyo/visited")
	qdb, err = badger.Open(opts)
	if err != nil {
		return errors.Wrap(err, "could not open key-value storage")
	}
	defer closedb(qdb)
	db, err := db.New(&conf.DB)
	if err != nil {
		return errors.Wrap(err, "could not connect to database")
	}
	defer db.Close()

	var set storage.URLSet
	if flags.redis {
		set = storage.NewRedisURLSet(redis.NewClient(conf.RedisOpts()))
	} else {
		set = storage.NewBadgerURLSet(qdb)
	}

	var (
		// vis     = visitor.New(db)
		vis = &visitor.FSVisitor{
			Base:  "./crawl-output",
			Hosts: make(map[string]struct{}),
		}
		ch      = make(chan *web.PageRequest, conf.QueueSize)
		crawler = web.NewCrawler(
			web.WithVisitor(vis),
			web.WithLimit(config.GetUint32("depth")),
			web.WithQueue(ch),
			web.WithSleep(conf.Sleep),
			web.WithDB(qdb),
			web.WithURLSet(set),
		)
	)

	web.UserAgent = "Remora"
	if len(conf.Seeds) < 1 {
		return errors.New("no seed URLs")
	}
	visitor.AddHosts(vis, conf.AllowedHosts)

	go runtimeCommandHandler(ctx, stop, crawler)
	for _, seed := range conf.Seeds {
		u, err := url.Parse(seed)
		if err != nil {
			log.Error(err)
			continue
		}
		visitor.AddHost(vis, u.Host)
		log.Infof("crawler enqueue %s", seed)
		crawler.Enqueue(web.NewPageRequest(u, 0))
	}

	log.WithFields(logrus.Fields{
		"seeds": conf.Seeds, "max_depth": config.GetUint("depth"),
	}).Info("Starting Crawl")

	crawler.Add(1)
	go crawler.Crawl(ctx, conf.RequestLimit)

	select {
	case <-sigs:
		stop()
		close(ch)
	case <-ctx.Done():
	}
	err = crawler.Close()
	if err != nil {
		log.WithError(err).Error("could not close web crawler")
	}

	log.WithFields(logrus.Fields{
		"duration": time.Since(start),
		"total":    crawler.N(),
	}).Info("Stopping Crawler")
	return nil
}

const DataDir = "/var/local/remora"

func initConfig(c *cmd.Config) {
	config.AddFile("remora.yml")
	config.AddFile("config.yml")
	config.AddPath(".")
	config.AddPath("/var/local/diktyo")
	config.AddPath(DataDir)
	config.SetType("yaml")
	config.SetConfig(c)
	err := config.InitDefaults()
	if err != nil {
		log.Fatal(errors.Wrap(err, "could not set config defaults"))
	}
	if err = config.ReadConfigFile(); err != nil {
		log.Fatal(errors.Wrap(err, "could not read config"))
	}
	for _, path := range []string{
		"/var/local/diktyo",
		DataDir,
	} {
		if _, err = os.Stat(path); os.IsNotExist(err) {
			err = os.MkdirAll(path, 3777)
			if err != nil {
				log.WithError(err).Error("could not create runtime directory")
			}
		}
	}
}

func initLogger(lvl logrus.Level) {
	log.SetOutput(io.Discard)
	log.SetLevel(logrus.TraceLevel)
	log.SetFormatter(&logging.PrefixedFormatter{
		Prefix: "",
		// TimeFormat:       time.RFC3339,
		TimeFormat:       time.Stamp,
		MaxMessageLength: 135,
		// NoColor:          true,
	})
	logfile.Filename = "/var/local/diktyo/crawler.log"
	log.AddHook(logging.NewLogFileHook(&logfile, &logrus.TextFormatter{
		DisableColors:   true,
		PadLevelText:    true,
		TimestampFormat: time.RFC3339,
	}))
	log.AddHook(&logging.Hook{
		Writer:    os.Stdout,
		LogLevels: logrus.AllLevels[:lvl+1],
	})
	web.SetLogger(log)
	visitor.SetLogger(log)
	frontier.SetLogger(log)
}

func closedb(db *badger.DB) {
	err := db.Close()
	if err != nil {
		log.WithError(err).Error("could not close persistant queue")
	}
	err = os.RemoveAll(db.Opts().Dir)
	if err != nil {
		log.WithError(err).Error("could not remove persistant queue")
	}
}

func newQueueListCmdFunc(conf *cmd.Config) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		queues, err := listQueues(
			conf.MessageQueue.Host,
			conf.MessageQueue.Management.Port,
		)
		if err != nil {
			return err
		}
		for _, q := range queues {
			fmt.Printf("%20s %s, %d\n", q.Name, q.State, q.Messages)
		}
		return nil
	}
}

type RabbitQueue struct {
	Name       string `json:"name"`
	VHost      string `json:"vhost"`
	Messages   int    `json:"messages"`
	State      string `json:"state"`
	AutoDelete bool   `json:"auto_delete"`
}

func listQueues(host string, port int) ([]RabbitQueue, error) {
	var url = fmt.Sprintf("http://%s:%d/api/queues/", host, port)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("guest", "guest")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	queues := make([]RabbitQueue, 0)
	err = json.NewDecoder(resp.Body).Decode(&queues)
	if err != nil {
		return nil, err
	}
	return queues, nil
}
