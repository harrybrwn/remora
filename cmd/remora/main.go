package main

import (
	"context"
	"fmt"
	"io"
	"net"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/internal/logging"
	"github.com/harrybrwn/diktyo/internal/rabbitmq"
	"github.com/harrybrwn/diktyo/internal/visitor"
	"github.com/harrybrwn/diktyo/web"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
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
	cmd := NewCLIRoot(&conf)
	conf.Bind(cmd.PersistentFlags())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = cmd.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewCLIRoot(conf *cmd.Config) *cobra.Command {
	var (
		configfile string
		noColor    bool
	)
	c := &cobra.Command{
		Use:   "remora",
		Short: "The internet's symbiotic organism.",
		Long:  "",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Parse the loglevel before reading the config file
			var (
				lvl logrus.Level = logrus.DebugLevel
				err error
			)
			level := conf.LogLevel
			if conf.LogLevel != "" {
				lvl = conf.GetLevel()
			}
			err = prerun(configfile, conf)
			if err != nil {
				return err
			}
			// If logLevel has changed in the config file but not as a flag
			// then parse the config file log level.
			if lvl == 0 || (level != conf.LogLevel && !cmd.Flags().Lookup("loglevel").Changed) {
				lvl = conf.GetLevel()
			}
			initLogger(noColor, lvl)
			return nil
		},
	}
	c.PersistentFlags().StringVar(&conf.LogLevel, "loglevel", conf.LogLevel, "set log level")
	c.PersistentFlags().StringVar(&configfile, "config", configfile, "use a different config file")
	c.PersistentFlags().BoolVar(&noColor, "no-color", noColor, "disable output colors")

	c.AddCommand(
		newSpiderCmd(conf),
		newPurgeCmd(conf),
		newEnqueueCmd(conf),
		newQueueCmd(conf),
		newRedisCmd(conf),
		newHitCmd(),
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
		&cobra.Command{
			Use: "test", Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				r := &net.Resolver{
					PreferGo: true,
					Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
						d := net.Dialer{
							Timeout: time.Millisecond * 10000,
						}
						return d.DialContext(ctx, network, "8.8.8.8:53")
					},
				}
				t := time.Now()
				ips, err := r.LookupHost(cmd.Context(), "www.google.com")
				if err != nil {
					return err
				}
				for _, ip := range ips {
					fmt.Println(ip)
				}
				fmt.Println(time.Since(t))
				return nil
			},
		},
		&cobra.Command{
			Use: "test-log", Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				var (
					wg  sync.WaitGroup
					n   = 30
					log = log.WithField("test", time.Now())
					fn  = []func(...interface{}){
						log.Trace, log.Debug, log.Info, log.Warn, log.Error,
					}
					l = len(fn)
				)
				wg.Add(n)
				for i := 0; i < n; i++ {
					func(i int) { fn[i%l](i); wg.Done() }(i)
				}
				wg.Wait()
				return nil
			},
		},
	)
	c.SetUsageTemplate(config.IndentedCobraHelpTemplate)
	return c
}

const DataDir = "/var/local/remora"

func prerun(configfile string, conf *cmd.Config) error {
	if configfile != "" {
		dir, file := filepath.Split(configfile)
		if dir == "" {
			dir = "."
		}
		if file == "" {
			return errors.New("no config file given")
		}
		config.AddPath(dir)
		config.AddFile(file)
	}
	config.AddFile("remora.yml")
	config.AddFile("config.yml")
	config.AddPath(".")
	config.AddPath("/var/local/diktyo")
	config.AddPath(DataDir)
	config.SetType("yaml")
	config.SetConfig(conf)
	err := config.InitDefaults()
	if err != nil {
		return errors.Wrap(err, "could not set config defaults")
	}
	if err = config.ReadConfig(); err != nil {
		return errors.Wrap(err, "could not read config")
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
	return nil
}

func initLogger(nocolor bool, lvl logrus.Level) {
	out := os.Stdout
	maxlen := 135
	if !logging.IsTerm(out) {
		nocolor = true
		maxlen = 1
	}
	log.SetOutput(io.Discard)
	log.SetLevel(logrus.TraceLevel)
	log.SetFormatter(&logging.PrefixedFormatter{
		Prefix: "",
		// TimeFormat:       time.RFC3339,
		TimeFormat:       time.Stamp,
		MaxMessageLength: maxlen,
		NoColor:          nocolor,
	})
	logfile.Filename = "/var/local/diktyo/crawler.log"
	log.AddHook(logging.NewLogFileHook(&logfile, &logrus.TextFormatter{
		DisableColors:   true,
		PadLevelText:    true,
		TimestampFormat: time.RFC3339,
	}))
	log.AddHook(&logging.Hook{
		Writer:    out,
		LogLevels: logrus.AllLevels[:lvl+1],
	})
	web.SetLogger(log)
	visitor.SetLogger(log)
	frontier.SetLogger(log)
}

func newQueueListCmdFunc(conf *cmd.Config) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ms := rabbitmq.ManagementServer{
			Host: conf.MessageQueue.Host,
			Port: conf.MessageQueue.Management.Port,
		}
		queues, err := ms.Queues()
		if err != nil {
			return err
		}
		for _, q := range queues {
			fmt.Printf("%20s %s, %d\n", q.Name, q.State, q.Messages)
		}
		return nil
	}
}
