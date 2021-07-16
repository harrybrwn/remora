package frontier

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/harrybrwn/diktyo/event"
	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"google.golang.org/protobuf/proto"
)

var log = logrus.New()

func SetLogger(l *logrus.Logger) {
	log = l
}

type Connect struct {
	Scheme   string
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	User     string `yaml:"user" json:"user"`
	Password string `yaml:"password" json:"password"`

	Config *amqp.Config
}

var (
	DefaultExchangeKind = "topic"
	ReconnectDelay      = time.Second * 5
)

type Exchange struct {
	Name       string
	Kind       string
	AutoDelete bool
	Durable    bool
}

type Frontier struct {
	Exchange Exchange
	config   Connect

	conn       *amqp.Connection
	connClosed chan *amqp.Error
	mu         sync.Mutex
	connected  *sync.Cond

	done  chan struct{}
	ready int32

	reconnectAfter  *time.Ticker
	reloadListeners []reloadable
}

func (e *Exchange) declare(ch *amqp.Channel) error {
	if e.Kind == "" {
		return nil
	}
	return ch.ExchangeDeclare(
		e.Name,
		e.Kind,
		e.Durable,
		e.AutoDelete,
		false, // internal
		false, // no wait
		nil,
	)
}

func (f *Frontier) Close() error {
	// close the done channel before closing the connection
	// so that any other objects that depend on knowing when
	// the session is closed are also notified before the
	// underlying connection is terminated
	close(f.done)
	return f.conn.Close()
}

func (f *Frontier) Connect(c Connect) error {
	f.connected = sync.NewCond(&f.mu)
	f.done = make(chan struct{})
	f.config = c
	f.reconnectAfter = time.NewTicker(ReconnectDelay)
	f.reloadListeners = make([]reloadable, 0)
	f.connected.L.Lock()
	defer f.connected.L.Unlock()
	go f.handleReconnect()
	for f.ready == 0 {
		f.connected.Wait()
	}
	return nil
}

func (f *Frontier) connect(c Connect) (*amqp.Connection, error) {
	f.connected.L.Lock()
	defer f.connected.L.Unlock()
	var (
		conn *amqp.Connection
		err  error
		uri  = uri(c)
	)
	if c.Config != nil {
		log.WithField("heartbeat", c.Config.Heartbeat).Debug("frontier: connecting with config")
		conn, err = amqp.DialConfig(uri, *c.Config)
	} else {
		conn, err = amqp.Dial(uri)
	}
	if err != nil {
		log.WithError(err).Error("frontier: could not connect to message queue")
		return nil, err
	}
	log.Debug("frontier: connection established")
	f.conn = conn
	f.connClosed = f.conn.NotifyClose(make(chan *amqp.Error))
	atomic.StoreInt32(&f.ready, 1)
	f.connected.Broadcast()
	return conn, err
}

func (f *Frontier) handleReconnect() {
	var done bool
	for {
		atomic.StoreInt32(&f.ready, 0)

		conn, err := f.connect(f.config)
		if err != nil {
			select {
			case <-f.done:
				log.Debug("frontier: done signal received")
				return
			case <-f.reconnectAfter.C:
				log.Debug("frontier: reconnecting...")
			}
			continue
		}
		done = f.reInit(conn)
		if done {
			log.Debug("frontier: done")
			break
		}
	}
}

func (f *Frontier) reInit(conn *amqp.Connection) bool {
	for {
		select {
		case <-f.done:
			log.Debug("frontier: done received")
			return true
		case <-f.connClosed:
			atomic.StoreInt32(&f.ready, 0)
			log.Debug("frontier: connection closed notification received")
			err := f.reload()
			if err != nil {
				log.WithError(err).Warn("frontier: reload failed")
			}
			return false
		}
	}
}

func (f *Frontier) reload() error {
	// Reload all listenters because the underlieing
	// connection has changed.
	var (
		err    error
		l      = len(f.reloadListeners) - 1
		closed = make([]bool, l+1)
	)
	log.Debug("frontier: reloading all listeners")
	defer log.Debug("frontier: all reloads sent")
	// reload all listeners and collect the closed ones
	for i, reload := range f.reloadListeners {
		err = reload.Reload()
		closed[i] = err != nil
	}
	// remove all listeners that have been closed
	for i := 0; i <= l; {
		if closed[i] {
			f.reloadListeners[i] = f.reloadListeners[l]
			f.reloadListeners = f.reloadListeners[:l]
			l--
		} else {
			i++
		}
	}
	return nil
}

// Used for creating channels but waiting for a
// connection to be ready.
func (f *Frontier) Channel() (*amqp.Channel, error) {
	f.connected.L.Lock()
	defer f.connected.L.Unlock()
	for f.ready == 0 {
		log.Debug("frontier: wating for connection to create channel")
		f.connected.Wait()
	}
	return f.conn.Channel()
}

func (f *Frontier) Consumer(queue string, opts ...event.ConsumerOpt) (event.Consumer, error) {
	c := &consumer{
		channel: channel{
			conn:     f,
			conndone: f.done,
			exchange: f.Exchange.Name,
		},
		autoAck: false,
		queue:   queue,
	}
	f.connected.L.Lock()
	for f.ready == 0 {
		f.connected.Wait()
		log.Debug("frontier: wating for connection to create consumer")
	}
	f.reloadListeners = append(f.reloadListeners, &c.channel)
	f.connected.L.Unlock()
	err := initChannel(context.TODO(), &c.channel)
	if err != nil {
		return nil, err
	}

	c.channel.wait() // wait for channel init
	err = f.Exchange.declare(c.ch)
	if err != nil {
		c.ch.Close()
		return nil, err
	}
	_, err = c.ch.QueueDeclare(queue, true, false, false, false, nil)
	if err != nil {
		c.ch.Close()
		return nil, err
	}
	for _, o := range opts {
		err = o(c)
		if err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (f *Frontier) Publisher(opts ...event.PublisherOpt) (event.Publisher, error) {
	p := &publisher{
		channel: channel{
			conn:     f,
			conndone: f.done,
			exchange: f.Exchange.Name,
		},
	}
	f.connected.L.Lock()
	for f.ready == 0 {
		f.connected.Wait()
		log.Debug("frontier: wating for connection to create publisher")
	}
	err := initChannel(context.TODO(), &p.channel)
	f.reloadListeners = append(f.reloadListeners, &p.channel)
	f.connected.L.Unlock()
	if err != nil {
		return nil, err
	}

	p.channel.wait()
	err = f.Exchange.declare(p.ch)
	if err != nil {
		p.Close()
		return nil, err
	}
	for _, o := range opts {
		err = o(p)
		if err != nil {
			p.ch.Close()
			return nil, err
		}
	}
	return p, nil
}

type publisher struct {
	channel
	mu sync.Mutex // prevent multi-publishing
}

func (p *publisher) Publish(key string, msg amqp.Publishing) error {
	select {
	case <-p.conndone:
		return errors.New("frontier connection is closed")
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.channel.Publish(p.exchange, key, false, false, msg)
	if err != nil {
		return err
	}
	return nil
}

type consumer struct {
	channel

	autoAck bool
	queue   string
	name    string
}

func WithAutoAck(auto bool) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		co.autoAck = auto
		return nil
	}
}

func WithPrefetch(n int) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		co.channel.prefetch = n
		return co.ch.Qos(n, 0, false)
	}
}

func WithName(name string) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		co.name = name
		return nil
	}
}

func WithKeys(keys ...string) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		if co.queue == "" {
			return errors.New("consumer queue not set")
		}
		return bindKeys(co, keys)
	}
}

func OnCancel(ch chan string) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		co.ch.NotifyCancel(ch)
		return nil
	}
}

func OnClose(ch chan *amqp.Error) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*consumer)
		if !ok {
			return errors.New("wrong consumer type")
		}
		co.ch.NotifyClose(ch)
		return nil
	}
}

func (c *consumer) WithOpt(opts ...event.ConsumerOpt) error {
	var err error
	for _, o := range opts {
		err = o(c)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *consumer) Consume(keys ...string) (<-chan amqp.Delivery, error) {
	select {
	case <-c.conndone:
		return nil, errors.New("connection is closed")
	default:
	}
	err := bindKeys(c, keys)
	if err != nil {
		return nil, err
	}
	return c.channel.Consume(c.queue, c.name, c.autoAck, false, false, false, nil)
}

func bindKeys(c *consumer, keys []string) error {
	var err error
	for _, k := range keys {
		err = c.ch.QueueBind(c.queue, k, c.exchange, false, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

const PageExchangeName = "page_topic"

func DeclarePageExchange(ch *amqp.Channel) error {
	return ch.ExchangeDeclare(
		PageExchangeName,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	)
}

func DeclareHostQueue(ch *amqp.Channel, host string) (amqp.Queue, error) {
	return ch.QueueDeclare(
		// fmt.Sprintf("spider.%s", host), // TODO add this
		host,
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no wait
		nil,   // arguments
	)
}

func PushRequest(ch *amqp.Channel, req *web.PageRequest) error {
	u, err := url.Parse(req.URL)
	if err != nil {
		return err
	}
	return PushRequestToHost(ch, req, u.Host)
}

type MessageType int

const (
	PageRequest MessageType = iota
)

func ParseMessageType(s string) MessageType {
	if s == "" {
		return PageRequest
	}
	t, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return PageRequest
	}
	return MessageType(t)
}

func (typ MessageType) String() string {
	return strconv.FormatInt(int64(typ), 10)
}

func SetPageReqAsMessageBody(req *web.PageRequest, msg *amqp.Publishing) error {
	raw, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	msg.Body = raw
	msg.Type = PageRequest.String()
	msg.ContentType = "application/vnd.google.protobuf"
	return nil
}

func PushRequestToHost(ch *amqp.Channel, req *web.PageRequest, name string) error {
	msg := amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		ContentType:  "application/vnd.google.protobuf",
		Priority:     0,
	}
	err := SetPageReqAsMessageBody(req, &msg)
	if err != nil {
		return err
	}
	return ch.Publish("", name, false, false, msg)
}

func uri(c Connect) string {
	if c.Scheme == "" {
		c.Scheme = "amqp"
	}
	if c.User == "" || c.Password == "" {
		return fmt.Sprintf("%s://%s:%d", c.Scheme, c.Host, c.Port)
	}
	return fmt.Sprintf("%s://%s:%s@%s:%d",
		c.Scheme,
		c.User, c.Password,
		c.Host, c.Port,
	)
}

func NewRequestQueue(ch *amqp.Channel) *requestQueue {
	return &requestQueue{ch}
}

type requestQueue struct{ ch *amqp.Channel }

func (q *requestQueue) Consume(ctx context.Context, name string) (<-chan *web.PageRequest, error) {
	ch := make(chan *web.PageRequest)
	delivery, err := q.ch.Consume(name, "", false, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	var (
		chanCancel = make(chan string)
	)
	q.ch.NotifyCancel(chanCancel)

	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-delivery:
				err = msg.Ack(false)
				if err != nil {
					log.WithError(err).Error("could not acknowledge message")
					continue
				}
				req := new(web.PageRequest)
				err = proto.Unmarshal(msg.Body, req)
				if err != nil {
					log.WithError(err).Error("could not unmarshal request")
					continue
				}
				ch <- req
			case tag := <-chanCancel:
				log.WithField("tag", tag)
				return
			}
		}
	}()
	return ch, nil
}
