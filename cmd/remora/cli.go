package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/db"
	"github.com/harrybrwn/remora/internal/rabbitmq"
	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/streadway/amqp"
)

func newPurgeCmd(conf *cmd.Config) *cobra.Command {
	c := &cobra.Command{
		Use:   "purge",
		Short: "Purge persistant storage used by the web crawler",
	}
	var del bool
	c.PersistentFlags().BoolVarP(&del, "delete", "d", del, "delete the queues instead of purgeing them")
	c.AddCommand(
		&cobra.Command{
			Use: "queue", Aliases: []string{"queues", "q"},
			Short: "Purge all rabbitMQ queues",
			RunE: func(cmd *cobra.Command, args []string) error {
				ms := rabbitmq.ManagementServer{
					Host: conf.MessageQueue.Host,
					Port: conf.MessageQueue.Management.Port,
				}
				fmt.Printf("%+v\n", ms)
				return purgeQueues(ms, conf.MessageQueue, del)
			},
		},
		&cobra.Command{
			Use:   "redis",
			Short: "Purge the redis cache used as a url set.",
			Long:  "Purge the redis cache used as a url set.",
			RunE: func(cmd *cobra.Command, args []string) error {
				return purgeRedis(conf, args)
			},
		},
		&cobra.Command{
			Use: "all", Short: "Purge all persistant storage",
			RunE: func(cmd *cobra.Command, args []string) error {
				err := purgeRedis(conf, []string{})
				if err != nil {
					return err
				}
				defer log.Info("purged redis")
				ms := rabbitmq.ManagementServer{
					Host: conf.MessageQueue.Host,
					Port: conf.MessageQueue.Management.Port,
				}
				return purgeQueues(ms, conf.MessageQueue, del)
			},
		},
	)
	return c
}

func newStatusCmd(conf *cmd.Config) *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Print the connection status of dependency services.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	return c
}

func newEnqueueCmd(use string, conf *cmd.Config) *cobra.Command {
	var (
		fromConfig bool
		useSitemap bool
	)
	c := &cobra.Command{
		// Use:   "enqueue",
		Use:   use,
		Short: "Send a url to the frontier",
		Long:  "Send a url to the frontier of the distributed crawler",
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				seeds = make(chan string)
				wg    sync.WaitGroup
			)
			if useSitemap {
				web.HttpClient.Timeout = time.Minute * 10 // yikes!
				wg.Add(len(args))
				go func() {
					wg.Wait()
					close(seeds)
					log.Info("closed url channel")
				}()
				for _, host := range args {
					log.Infof("getting sitemap for %q", host)
					go gatherSitemap(cmd.Context(), host, seeds, &wg)
				}
			} else {
				var urls []string
				if fromConfig {
					urls = conf.Seeds[:]
				} else {
					urls = args[:]
				}
				l := len(urls)
				if l < 1 {
					return errors.New("no urls to enqueue")
				}
				wg.Add(l)
				go func() {
					wg.Wait()
					close(seeds)
					log.Info("closed url channel")
				}()
				for _, s := range urls {
					go func(s string) {
						defer wg.Done()
						seeds <- s
					}(s)
				}
			}
			return enqueue(cmd.Context(), &conf.MessageQueue, seeds)
		},
	}
	c.Flags().BoolVar(&fromConfig, "from-config", fromConfig,
		"enqueue the seed urls defined in the config file")
	c.Flags().BoolVar(&useSitemap, "use-sitemap", useSitemap, "enqueue urls in the website's sitemap")
	return c
}

func newRedisCmd(conf *cmd.Config) *cobra.Command {
	newclient := func(ctx context.Context) (*redis.Client, error) {
		opts := conf.RedisOpts()
		opts.ReadTimeout = time.Minute
		client := redis.NewClient(opts)
		if err := client.Ping(ctx).Err(); err != nil {
			client.Close()
			return nil, errors.Wrap(err, "could not ping redis server")
		}
		return client, nil
	}
	c := &cobra.Command{
		Use:   "redis",
		Short: "Manage the redis database",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newclient(cmd.Context())
			if err != nil {
				fmt.Println("could not connect to redis")
				return nil
			}
			fmt.Println("redis connected.")
			return c.Close()
		},
	}

	c.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List all keys stored in the database",
			Aliases: []string{"l"}, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				client, err := newclient(ctx)
				if err != nil {
					return err
				}
				defer client.Close()
				keys, err := client.Keys(ctx, "*").Result()
				if err != nil {
					return err
				}
				for _, k := range keys {
					fmt.Fprintf(os.Stdout, "%s\n", k)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "get", Short: "Get a value from the database",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				client, err := newclient(ctx)
				if err != nil {
					return err
				}
				defer client.Close()
				res, err := client.Get(ctx, args[0]).Result()
				if err != nil {
					return err
				}
				fmt.Println(res)
				return nil
			},
		},
		&cobra.Command{
			Use: "has <key>", Short: "Test whether or not the database has a key",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				client, err := newclient(ctx)
				if err != nil {
					return err
				}
				defer client.Close()
				err = client.Get(ctx, args[0]).Err()
				fmt.Println(err != redis.Nil)
				return nil
			},
		},
		&cobra.Command{
			Use: "put <key> <value>", Short: "Add a value to the database",
			Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				cli, err := newclient(ctx)
				if err != nil {
					return err
				}
				defer cli.Close()
				return cli.Set(ctx, args[0], args[1], 0).Err()
			},
		},
		&cobra.Command{
			Use: "delete <key>", Short: "Remove a value from the database",
			Aliases: []string{"del"}, Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				cli, err := newclient(ctx)
				if err != nil {
					return err
				}
				defer cli.Close()
				for _, k := range args {
					err = cli.Del(ctx, k).Err()
					if err != nil {
						log.WithError(err).Warning("could not delete " + k)
					}
				}
				return nil
			},
		},
	)
	return c
}

func newHitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hit",
		Short: "Send a head request.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendHead(args[0])
		},
	}
	return c
}

func newQueueCmd(conf *cmd.Config) *cobra.Command {
	c := &cobra.Command{
		Use: "queue", Aliases: []string{"q"},
		Short: "Manage the message queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := "down"
			conn, err := amqp.Dial(conf.MessageQueue.URI())
			if err == nil {
				status = "up"
			}
			cmd.Printf("%v status: %s\n", conf.MessageQueue.URI(), status)
			if conn == nil {
				return nil
			}
			if err = conn.Close(); err != nil {
				return err
			}
			ms := rabbitmq.ManagementServer{
				Host: conf.MessageQueue.Host,
				Port: conf.MessageQueue.Management.Port,
			}
			overview, err := ms.Overview()
			if err != nil {
				return err
			}
			tot := overview.ObjectTotals
			tab := tabwriter.NewWriter(os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape)
			tab.Write([]byte("CONNECTIONS\tCHANNELS\tEXCHANGES\tQUEUES\tCONSUMERS\n"))
			tab.Write([]byte(strings.Join([]string{
				strconv.FormatInt(int64(tot.Connections), 10),
				strconv.FormatInt(int64(tot.Channels), 10),
				strconv.FormatInt(int64(tot.Exchanges), 10),
				strconv.FormatInt(int64(tot.Queues), 10),
				strconv.FormatInt(int64(tot.Consumers), 10),
			}, "\t") + "\n"))
			fmt.Println()
			return tab.Flush()
		},
	}
	c.AddCommand(
		newEnqueueCmd("put", conf),
		&cobra.Command{
			Use: "list", Short: "List all the queues",
			RunE: newQueueListCmdFunc(conf),
		},
		&cobra.Command{
			Use: "channels", Short: "List all channels",
			RunE: func(cmd *cobra.Command, args []string) error {
				ms := rabbitmq.ManagementServer{
					Host: conf.MessageQueue.Host,
					Port: conf.MessageQueue.Management.Port,
				}
				channels, err := ms.Channels()
				if err != nil {
					return err
				}
				tab := tabwriter.NewWriter(os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape)
				tab.Write([]byte(strings.Join([]string{
					"NAME",
					"NUMBER",
					"DELIVER-RATE",
				}, "\t") + "\n"))
				for _, c := range channels {
					tab.Write([]byte(strings.Join([]string{
						c.Name,
						strconv.FormatInt(int64(c.Number), 10),
						strconv.FormatFloat(float64(c.MessageStats.DeliverDetails.Avg), 'f', -1, 64),
					}, "\t") + "\n"))
				}
				return tab.Flush()
			},
		},
		&cobra.Command{
			Use: "connections", Short: "List rabbitmq connections",
			Aliases: []string{"conn", "c"},
			RunE: func(cmd *cobra.Command, args []string) error {
				ms := rabbitmq.ManagementServer{
					Host: conf.MessageQueue.Host,
					Port: conf.MessageQueue.Management.Port,
				}
				tab := tabwriter.NewWriter(os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape)
				conns, err := ms.Connections()
				if err != nil {
					return err
				}
				tab.Write([]byte(strings.Join([]string{
					"NAME",
					"STATE",
					"PEERHOST",
					// "CONNECTED-AT",
					"PROTOCOL",
					"RECV-CNT",
				}, "\t") + "\n"))
				for _, c := range conns {
					tab.Write([]byte(strings.Join([]string{
						c.Name,
						c.State,
						c.PeerHost,
						// time.Unix(0, c.ConnectedAt).String(),
						c.Protocol,
						strconv.FormatInt(int64(c.RecvCnt), 10),
					}, "\t") + "\n"))
				}
				err = tab.Flush()
				if err != nil {
					return err
				}
				fmt.Printf("connections: %d\n", len(conns))
				return nil
			},
		},
	)
	return c
}

func newPSQLCmd(conf *cmd.Config) *cobra.Command {
	return &cobra.Command{
		Use: "psql [psql arguments...]", Short: "Run psql using the data from the config",
		// DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			db := conf.DB
			arguments := []string{
				"-h", db.Host,
				"-p", strconv.Itoa(db.Port),
				db.Name, db.User,
			}
			arguments = append(arguments, args...)
			c := exec.Command("psql", arguments...)
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			c.Stdin = os.Stdin
			return c.Run()
		},
	}
}

func newDBCmd(conf *cmd.Config) *cobra.Command {
	return &cobra.Command{
		Use: "db",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := db.New(&conf.DB)
			if err != nil {
				return err
			}
			defer db.Close()
			if err = db.Ping(); err != nil {
				return err
			}
			fmt.Println("database is up")
			return nil
		},
	}
}

func runListCmd(cmd *cobra.Command, args []string) error {
	page := web.NewPageFromString(args[0], 0)
	err := page.FetchCtx(cmd.Context())
	if err != nil {
		return err
	}
	for _, l := range page.Links {
		fmt.Println(l)
	}
	fmt.Println(len(page.Links), "links found")
	return nil
}

func runKeywordsCmd(cmd *cobra.Command, args []string) error {
	p := web.NewPageFromString(args[0], 0)
	err := p.FetchCtx(cmd.Context())
	if err != nil {
		return err
	}
	k, err := p.Keywords()
	if err != nil {
		return err
	}
	cmd.Printf("%s\n", strings.Join(k, " "))
	return nil
}

func toMB(bytes uint64) float64 {
	return float64(bytes) / 1024.0 / 1024.0
}
