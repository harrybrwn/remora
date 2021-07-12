package main

import (
	"fmt"
	"net/url"
	"os"
	"sync"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/web"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/streadway/amqp"
	"google.golang.org/protobuf/proto"
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
				return purgeQueues(conf.MessageQueue, del)
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
				return purgeQueues(conf.MessageQueue, del)
			},
		},
	)
	return c
}

func newEnqueueCmd(conf *cmd.Config) *cobra.Command {
	var fromConfig bool
	c := &cobra.Command{
		Use:   "enqueue",
		Short: "Send a url to the frontier",
		Long:  "Send a url to the frontier of the distributed crawler",
		RunE: func(cmd *cobra.Command, args []string) error {
			seeds := args[:]
			if fromConfig {
				seeds = conf.Seeds
			}
			if len(seeds) < 1 {
				return errors.New("no seed urls given")
			}
			log.Infof("enqueue %v", seeds)
			return enqueue(&conf.MessageQueue, seeds...)
		},
	}
	c.Flags().BoolVar(&fromConfig, "from-config", fromConfig,
		"enqueue the seed urls defined in the config file")
	return c
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

func purgeQueues(conf cmd.MessageQueueConfig, del bool) error {
	queues, err := listQueues(
		conf.Host,
		conf.Management.Port,
	)
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
		go func(q RabbitQueue) {
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

func enqueue(conf *cmd.MessageQueueConfig, seeds ...string) error {
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
	err = front.Connect(frontier.Connect{Host: conf.Host, Port: conf.Port})
	if err != nil {
		return err
	}
	defer front.Close()
	publisher, err := front.Publisher()
	if err != nil {
		return err
	}

	wg.Add(len(seeds))
	for _, seed := range seeds {
		req := web.ParsePageRequest(seed, 0)
		go func() {
			defer wg.Done()
			u, err := url.Parse(req.URL)
			if err != nil {
				log.WithError(err).Error("could not parse seed url")
				return
			}
			// q, err := frontier.DeclareHostQueue(ch, u.Host)
			// if err != nil {
			// 	log.WithError(err).Errorf("could not declare queue for %q", u.Host)
			// 	return
			// }

			rawreq, err := proto.Marshal(req)
			if err != nil {
				log.WithError(err)
				return
			}
			msg := amqp.Publishing{
				Body:         rawreq,
				DeliveryMode: amqp.Persistent,
				ContentType:  "application/vnd.google.protobuf",
				Type:         frontier.PageRequest.String(),
				Priority:     0,
			}
			// err = ch.Publish(
			// 	"",
			// 	// "any",
			// 	q.Name,
			// 	false, false,
			// 	msg,
			// )
			fmt.Println(u.Host)
			err = publisher.Publish(u.Host, msg)
			// err = frontier.PushRequestToHost(ch, &req, q.Name)
			if err != nil {
				log.WithError(err).Error("could not publish url request")
				return
			}
		}()
	}
	wg.Wait()
	return nil
}
