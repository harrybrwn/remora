package cmd

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/db"
	flag "github.com/spf13/pflag"
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

	// Profile  string `yaml:"profile" config:"profile"`
	Profiles map[string]struct {
		Seeds        []string `yaml:"seeds" config:"seeds"`
		AllowedHosts []string `yaml:"allowed_hosts" config:"allowed_hosts"`
	} `yaml:"profiles"`
	MessageQueue MessageQueueConfig `yaml:"message_queue" config:"message_queue"`

	VisitedSet struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		DB       int    `yaml:"db"`
		Password string `yaml:"password"`
	} `yaml:"visited_set" config:"visited_set"`
}

func (c *Config) Bind(flag *flag.FlagSet) {
	flag.Int64Var(&c.QueueSize, "queue", c.QueueSize, "Size of the main queue")
	// flag.StringArrayVarP(&c.AllowedHosts, "allowed-hosts", "a", c.AllowedHosts,
	// 	"A list of hosts that the crawler is allowed to "+
	// 		"visit. The host of seed urls are added implicitly")
	flag.DurationVar(&c.Sleep, "sleep", c.Sleep, "Sleep time for each spider request")
	flag.UintVar(&c.Depth, "depth", c.Depth, "Crawler depth limit")
	// flag.StringVar(&c.Profile, "profile", "", "config profile to use for the crawler")
}

func (c *Config) RedisOpts() *redis.Options {
	return &redis.Options{
		Addr: net.JoinHostPort(
			c.VisitedSet.Host,
			strconv.FormatInt(int64(c.VisitedSet.Port), 10),
		),
		DB:       c.VisitedSet.DB,
		Password: c.VisitedSet.Password,
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
	if mqc.User == "" || mqc.Password == "" {
		return fmt.Sprintf("amqp://%s:%d", mqc.Host, mqc.Port)
	}
	return fmt.Sprintf("amqp://%s:%s@%s:%d",
		mqc.User,
		mqc.Password,
		mqc.Host,
		mqc.Port,
	)
}
