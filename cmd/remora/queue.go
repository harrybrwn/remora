package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal/rabbitmq"
	"github.com/harrybrwn/remora/web"
	"github.com/spf13/cobra"
	"github.com/streadway/amqp"
)

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

func newQueueListCmdFunc(conf *cmd.Config) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ms := rabbitmq.ManagementServer{
			Host: conf.MessageQueue.Host,
			Port: conf.MessageQueue.Management.Port,
		}
		tab := tabwriter.NewWriter(os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape)
		queues, err := ms.Queues()
		if err != nil {
			return err
		}
		_, err = tab.Write([]byte("\tNAME\tSTATE\tMESSAGES\tMESSAGE-RATE\tINCOMING\tACK-RATE\tCONSUMER-UTILIZATION\n"))
		if err != nil {
			return err
		}

		for _, q := range queues {
			_, err = tab.Write([]byte(fmt.Sprintf("\t%s\t%q\t%d\t%f\t%f\t%f\t%f\n",
				q.Name, q.State, q.Messages,
				q.MessagesDetails.Rate,
				q.MessageStats.PublishDetails.Rate,
				q.MessageStats.AckDetails.Rate,
				q.ConsumerUtilisation,
			)))
			if err != nil {
				return err
			}
		}
		err = tab.Flush()
		if err != nil {
			return err
		}
		fmt.Printf("queues: %d\n", len(queues))
		return nil
	}
}

func enqueue(ctx context.Context, conf *cmd.MessageQueueConfig, seeds <-chan string) error {
	var wg sync.WaitGroup
	front := frontier.Frontier{Exchange: exchange}
	err := front.Connect(ctx, frontier.Connect{Host: conf.Host, Port: conf.Port})
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

				err = publish(pub, fnv.New128(), u.Host, req)
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
