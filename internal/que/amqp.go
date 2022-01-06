package que

import (
	"crypto/tls"
	"io"
	"net"
	"time"

	"github.com/streadway/amqp"
)

func AMQPDial(uri string) (AMQPConnection, error) {
	conn, err := amqp.DialConfig(uri, amqp.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
		Dial:      amqp.DefaultDial(time.Second * 60),
	})
	if err != nil {
		return nil, err
	}
	return &amqpConn{Connection: conn}, nil
}

type (
	// Structures
	Delivery         = amqp.Delivery
	Publishing       = amqp.Publishing
	AMQPError        = amqp.Error
	AMQPTable        = amqp.Table
	AMQPQueue        = amqp.Queue
	AMQPBlocking     = amqp.Blocking
	AMQPReturn       = amqp.Return
	AMQPConfirmation = amqp.Confirmation
	// Interfaces
	Acknowledger = amqp.Acknowledger
)

type AMQPConnection interface {
	io.Closer
	Channel() (AMQPChannel, error)
	LocalAddr() net.Addr
	IsClosed() bool
	ConnectionState() tls.ConnectionState

	NotifyClose(chan *AMQPError) chan *AMQPError
	NotifyBlocked(chan AMQPBlocking) chan AMQPBlocking
}

var (
	ExchangeDirect  = amqp.ExchangeDirect
	ExchangeFanout  = amqp.ExchangeFanout
	ExchangeTopic   = amqp.ExchangeTopic
	ExchangeHeaders = amqp.ExchangeHeaders
)

type AMQPChannel interface {
	AMQPConsumer
	AMQPPublisher
	AMQPTxChannel

	// Set the prefetch-count, prefetch-size. Includes flag for global effects.
	Qos(count, size int, global bool) error
	Cancel(string, bool) error
	Get(queue string, autoack bool) (Delivery, bool, error)
	Flow(active bool) error

	QueueDeclare(name string, durable, autodel, exclusive, nowait bool, args AMQPTable) (AMQPQueue, error)
	QueueDeclarePassive(name string, durable, autodel, excl, nowait bool, args AMQPTable) (AMQPQueue, error)
	QueueBind(name, key, exchange string, nowait bool, args AMQPTable) error
	QueueInspect(name string) (AMQPQueue, error)
	QueueUnbind(name, key, exchange string, args AMQPTable) error
	QueuePurge(name string, noWait bool) (int, error)
	QueueDelete(name string, ifUnused, ifEmpty, nowait bool) (int, error)

	ExchangeDeclare(name, kind string, durable, autoDel, internal, nowait bool, args AMQPTable) error
	ExchangeDelete(name string, ifUnused, noWait bool) error
	ExchangeBind(dest, key, source string, noWait bool, args AMQPTable) error
	ExchangeUnbind(dest, key, source string, noWait bool, args AMQPTable) error

	NotifyClose(chan *AMQPError) chan *AMQPError
	NotifyCancel(chan string) chan string
	NotifyFlow(c chan bool) chan bool
	NotifyReturn(c chan AMQPReturn) chan AMQPReturn
	NotifyConfirm(ack, nack chan uint64) (chan uint64, chan uint64)
	NotifyPublish(confirm chan AMQPConfirmation) chan AMQPConfirmation

	Recover(requeue bool) error
	Reject(tag uint64, requeue bool) error
}

type AMQPConsumer interface {
	io.Closer
	// Start consuming on the channel
	Consume(queue, consumer string, autoack, excl, nolocal, nowait bool, args AMQPTable) (<-chan Delivery, error)
}

type AMQPPublisher interface {
	io.Closer
	Publish(exchange, key string, manditory, immediate bool, msg Publishing) error
}

// AMQPTxChannel describes a channel that can be put into "transaction" mode for
// server changes to be committed or rolled back.
type AMQPTxChannel interface {
	// Put the channel in transaction mode
	Tx() error
	TxCommit() error
	TxRollback() error
}

type amqpConn struct {
	*amqp.Connection
}

func (c *amqpConn) Channel() (AMQPChannel, error) {
	return c.Connection.Channel()
}
