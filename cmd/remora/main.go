package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/harrybrwn/remora/internal/visitor"
	"github.com/harrybrwn/remora/web"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/natefinch/lumberjack.v2"
)

//go:generate cp ../../LICENSE .

//go:embed LICENSE
var license []byte

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
		err error
		cmd = NewCLIRoot()
	)
	godotenv.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = cmd.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewCLIRoot() *cobra.Command {
	var (
		configfile string
		noColor    bool
		conf       cmd.Config
	)
	c := &cobra.Command{
		Use:           "remora",
		Short:         "The internet's symbiotic organism.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long:          "",
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
			if err = prerun(configfile, &conf); err != nil {
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
	flags := c.PersistentFlags()
	flags.StringVar(&conf.LogLevel, "loglevel", conf.LogLevel, "set log level")
	flags.StringVarP(&configfile, "config", "c", configfile, "use a different config file")
	flags.BoolVar(&noColor, "no-color", noColor, "disable output colors")

	confcmd := config.NewConfigCommand()
	config.SetDefaultCommandFlags(confcmd)
	c.AddCommand(
		newCrawlCmd(&conf),
		newSpiderCmd(&conf),
		newPurgeCmd(&conf),
		newEnqueueCmd(&conf),
		newQueueCmd(&conf),
		newRedisCmd(&conf),
		newDBCmd(&conf),
		newHitCmd(),
		cmd.NewVersionCmd(),
		confcmd,
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
			RunE: func(cmd *cobra.Command, args []string) error { return nil },
		},
		&cobra.Command{Use: "license", Run: func(cmd *cobra.Command, args []string) { fmt.Printf("%s\n", license) }},
	)
	c.SetOut(os.Stdout)
	c.SetErr(os.Stderr)
	c.SetUsageTemplate(config.IndentedCobraHelpTemplate)
	conf.Bind(c.PersistentFlags())
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
	config.AddUserConfigDir("remora")
	config.AddFile("remora.yml")
	config.AddFile("config.yml")
	config.AddPath(".")
	config.AddPath(DataDir)
	config.SetType("yaml")
	config.SetConfig(conf)
	err := config.InitDefaults()
	if err != nil {
		return errors.Wrap(err, "could not set config defaults")
	}

	err = config.ReadConfig()
	switch err {
	case nil:
		break
	case config.ErrNoConfigFile:
		log.WithFields(logrus.Fields{"error": err}).Warn("could not read config file")
	default:
		return errors.Wrap(err, "could not read config")
	}

	for _, path := range []string{
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
	maxlen := 125
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
	logfile.Filename = filepath.Join(DataDir, "crawler.log")
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
