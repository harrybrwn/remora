package main

import "github.com/streadway/amqp"

func NewMessageQueue(name, uri string) (*MessageQueue, error) {
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &MessageQueue{
		conn:  conn,
		ch:    ch,
		queue: name,
	}, nil
}

type MessageQueue struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	queue string

	Durable    bool
	AutoDelete bool
	Exclusive  bool
	NoWait     bool
}

func (q *MessageQueue) Init() error {
	_, err := q.ch.QueueDeclare(q.queue, false, false, false, false, nil)
	if err != nil {
		return err
	}
	// return q.ch.QueueBind(q.queue, "#", "crawl-events", false, nil)
	return nil
}

func (q *MessageQueue) Close() (err error) {
	err = q.ch.Close()
	if err != nil {
		return err
	}
	return q.conn.Close()
}

func (q *MessageQueue) Channel() *amqp.Channel { return q.ch }
