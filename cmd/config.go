package cmd

import (
	"net"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/harrybrwn/remora/db"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"github.com/streadway/amqp"
)

type Config struct {
	DB           db.Config `yaml:"db" config:"db"`
	QueueSize    int64     `yaml:"queue_size" default:"1"`
	Seeds        []string  `yaml:"seeds"`
	Depth        uint      `yaml:"depth" config:"depth" default:"2"`
	AllowedHosts []string  `yaml:"allowed_hosts" config:"allowed_hosts"`
	LogLevel     string    `yaml:"loglevel" default:"debug"`
	// A limit on the number of concurrent requests being
	/// waited for at any moment
	RequestLimit int           `yaml:"request_limit"`
	Sleep        time.Duration `yaml:"sleep"`
	// A map of hostnames and the time that each should wait
	// netween page fetches.
	// defaults to the Sleep variable
	WaitTimes map[string]string `yaml:"wait_times"`

	Profiles map[string]struct {
		Seeds        []string `yaml:"seeds" config:"seeds"`
		AllowedHosts []string `yaml:"allowed_hosts" config:"allowed_hosts"`
	} `yaml:"profiles"`
	MessageQueue MessageQueueConfig `yaml:"message_queue" config:"message_queue"`
	// Redis config
	VisitedSet struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		DB       int    `yaml:"db"`
		Password string `yaml:"password"`
	} `yaml:"visited_set" config:"visited_set,notflag"`
	// Opentelemetry tracing config
	Tracer    TracerConfig `yaml:"tracer" config:"tracer,notflag"`
	UserAgent string       `yaml:"user_agent" config:"user_agent" default:"Remora"`
}

func (c *Config) Bind(flag *flag.FlagSet) {
	flag.Int64Var(&c.QueueSize, "queue", c.QueueSize, "Size of the main queue")
	// flag.StringArrayVarP(&c.AllowedHosts, "allowed-hosts", "a", c.AllowedHosts,
	// 	"A list of hosts that the crawler is allowed to "+
	// 		"visit. The host of seed urls are added implicitly")
	// flag.DurationVar(&c.Sleep, "sleep", c.Sleep, "Sleep time for each spider request")
	flag.UintVar(&c.Depth, "depth", c.Depth, "Crawler depth limit")
}

func (c *Config) RedisOpts() *redis.Options {
	return &redis.Options{
		Addr: net.JoinHostPort(
			c.VisitedSet.Host,
			strconv.FormatInt(int64(c.VisitedSet.Port), 10),
		),
		DB:          c.VisitedSet.DB,
		Password:    c.VisitedSet.Password,
		ReadTimeout: time.Second * 30,
	}
}

type MessageQueueConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port" default:"5672"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`

	Management struct {
		Port     int    `yaml:"port" default:"15672"`
		User     string `yaml:"user" default:"guest"`
		Password string `yaml:"password" default:"guest"`
	} `yaml:"management"`

	Prefetch int `yaml:"prefetch"`
}

func (mqc *MessageQueueConfig) URI() string {
	return amqp.URI{
		Scheme:   "amqp",
		Host:     mqc.Host,
		Port:     mqc.Port,
		Username: mqc.User,
		Password: mqc.Password,
	}.String()
}

type TracerConfig struct {
	Type     string `yaml:"type" config:"type"` // jaeger, zipkin, datadog
	Endpoint string `yaml:"endpoint" config:"endpoint"`
	Service  string `yaml:"service" config:"service"`
}

func (c *Config) GetLevel() logrus.Level {
	lvl, err := logrus.ParseLevel(c.LogLevel)
	if err != nil {
		logrus.WithError(err).Warn("could not parse log level")
		return logrus.DebugLevel
	}
	return lvl
}

var (
	version    = "dev"
	date       string
	commit     string
	sourcehash string
)

func GetVersionInfo() *VersionInfo {
	return &VersionInfo{
		Version:    version,
		Date:       date,
		Commit:     commit,
		SourceHash: sourcehash,
	}
}

type VersionInfo struct {
	Version    string
	Date       string
	Commit     string
	SourceHash string
}

func NewVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Show the command version",
		Aliases: []string{"v"},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			if version == "dev" {
				cmd.Printf("%s development version\n", root.Name())
				return nil
			}
			cmd.Printf("%s version %s\n", root.Name(), version)
			cmd.Printf("date:   %s\n", date)
			cmd.Printf("commit: %s\n", commit)
			if sourcehash != "" {
				cmd.Printf("hash:   %s\n", sourcehash)
			}
			return nil
		},
	}
}
