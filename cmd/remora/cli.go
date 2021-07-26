package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/internal/rabbitmq"
	"github.com/harrybrwn/diktyo/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
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

func newEnqueueCmd(conf *cmd.Config) *cobra.Command {
	var (
		fromConfig bool
		useSitemap bool
	)
	c := &cobra.Command{
		Use:   "enqueue",
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

func newRedisCmd(conf *cmd.Config) *cobra.Command {
	newclient := func() (*redis.Client, error) {
		client := redis.NewClient(conf.RedisOpts())
		if err := client.Ping().Err(); err != nil {
			client.Close()
			return nil, errors.Wrap(err, "could not ping redis server")
		}
		return client, nil
	}
	c := &cobra.Command{
		Use:   "redis",
		Short: "Manage the redis database",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newclient()
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
			Use: "list", Aliases: []string{"l"},
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newclient()
				if err != nil {
					return err
				}
				defer client.Close()
				keys, err := client.Keys("*").Result()
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
			Use:  "get",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newclient()
				if err != nil {
					return err
				}
				defer client.Close()
				res, err := client.Get(args[0]).Result()
				if err != nil {
					return err
				}
				fmt.Println(res)
				return nil
			},
		},
		&cobra.Command{
			Use:  "has",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newclient()
				if err != nil {
					return err
				}
				defer client.Close()
				err = client.Get(args[0]).Err()
				fmt.Println(err != redis.Nil)
				return nil
			},
		},
		&cobra.Command{
			Use:  "put <key> <value>",
			Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cli, err := newclient()
				if err != nil {
					return err
				}
				defer cli.Close()
				return cli.Set(args[0], args[1], 0).Err()
			},
		},
		&cobra.Command{
			Use: "delete", Aliases: []string{"del"},
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cli, err := newclient()
				if err != nil {
					return err
				}
				defer cli.Close()
				for _, k := range args {
					err = cli.Del(k).Err()
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func newHitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hit",
		Short: "",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequest("HEAD", args[0], nil)
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
			return conn.Close()
		},
	}
	c.AddCommand(
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
				for _, c := range channels {
					fmt.Println(c.Name, c.Number, c.MessageStats.DeliverGetDetails.Rate)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "peek", Short: "Peek at the front of a queue",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return nil
			},
		},
	)
	return c
}

func purgeRedis(conf *cmd.Config, args []string) error {
	c := redis.NewClient(conf.RedisOpts())
	if err := c.Ping().Err(); err != nil {
		return err
	}
	if len(args) == 0 {
		return c.FlushDB().Err()
	}
	return c.Del(args...).Err()
}

func purgeQueues(ms rabbitmq.ManagementServer, conf cmd.MessageQueueConfig, del bool) error {
	queues, err := ms.Queues()
	if err != nil {
		return err
	}
	conn, err := amqp.Dial(conf.URI())
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	var fn = func(name string) (int, error) { return ch.QueuePurge(name, false) }
	if del {
		fn = func(name string) (int, error) { return ch.QueueDelete(name, false, false, false) }
	}

	var wg sync.WaitGroup
	wg.Add(len(queues))
	for _, queue := range queues {
		go func(q rabbitmq.Queue) {
			defer wg.Done()
			n, err := fn(q.Name)
			if err != nil {
				log.WithError(err).WithField("name", q.Name).Error("could not purge queue")
				return
			}
			log.Infof("purged queue %s having %d messages", q.Name, n)
		}(queue)
	}
	wg.Wait()
	return nil
}

func enqueue(ctx context.Context, conf *cmd.MessageQueueConfig, seeds <-chan string) error {
	var wg sync.WaitGroup
	conn, err := amqp.Dial(conf.URI())
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	err = frontier.DeclarePageExchange(ch)
	if err != nil {
		log.WithError(err).Error("could not declare exchange")
		return err
	}

	front := frontier.Frontier{
		Exchange: frontier.Exchange{
			Name:    frontier.PageExchangeName,
			Kind:    "topic",
			Durable: true,
		},
	}
	err = front.Connect(ctx, frontier.Connect{Host: conf.Host, Port: conf.Port})
	if err != nil {
		return err
	}
	defer front.Close()
	pub, err := front.Publisher()
	if err != nil {
		return err
	}

	spinCtx, cancelSpinner := context.WithCancel(ctx)
	go func() {
		var (
			i uint64
			c byte
		)
		for {
			select {
			case <-spinCtx.Done():
				return
			default:
			}
			switch i % 4 {
			case 0:
				c = '\\'
			case 1:
				c = '|'
			case 2:
				c = '/'
			case 3:
				c = '-'
			}
			fmt.Printf("\r%c", c)
			time.Sleep(time.Millisecond * 50)
			i++
		}
	}()

	n := 0
	for {
		select {
		case <-ctx.Done():
			cancelSpinner()
			return nil
		case seed, ok := <-seeds:
			if !ok {
				goto done
			}
			wg.Add(1)
			req := web.ParsePageRequest(seed, 0)
			n++
			go func() {
				defer wg.Done()
				u, err := url.Parse(req.URL)
				if err != nil {
					log.WithError(err).Error("could not parse seed url")
					return
				}

				hash := fnv.New128()
				// io.WriteString(hash, u.Host)
				// rawreq, err := proto.Marshal(req)
				// if err != nil {
				// 	log.WithError(err)
				// 	return
				// }
				// msg := amqp.Publishing{
				// 	Body:         rawreq,
				// 	DeliveryMode: amqp.Persistent,
				// 	ContentType:  "application/vnd.google.protobuf",
				// 	Type:         frontier.PageRequest.String(),
				// 	Priority:     0,
				// }
				// key := fmt.Sprintf("%x.%s", hash.Sum(nil)[:2], u.Host)
				// err = pub.Publish(key, msg)

				err = publish(pub, hash, u.Host, req)
				if err != nil {
					log.WithError(err).Error("could not publish url request")
					return
				}
			}()
		}
	}
done:
	wg.Wait()
	cancelSpinner()
	fmt.Print("\r")
	log.Infof("queued %d pages", n)
	return nil
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

func getMemStats() *runtime.MemStats {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return &s
}

func toMB(bytes uint64) float64 {
	return float64(bytes) / 1024.0 / 1024.0
}

func setLogLevelRunFunc(_ *cobra.Command, args []string) error {
	if len(args) < 1 {
		return errors.New("no level given")
	}
	lvl, err := logrus.ParseLevel(args[0])
	if err != nil {
		return err
	}
	log.SetLevel(lvl)
	return nil
}
