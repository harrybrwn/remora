package queue

import (
	"encoding/json"
	"sync"

	"github.com/dgraph-io/badger/v3"
)

type Queue interface {
	Put([]byte) error
	Pop() ([]byte, error)
	Size() int
}

func New(folder string) (Queue, error) {
	opts := badger.DefaultOptions(folder)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &queue{db: db, ch: make(chan struct{})}, nil
}

type queue struct {
	db         *badger.DB
	ch         chan struct{}
	mu         sync.RWMutex
	head, tail []byte
	size       int
}

type node struct {
	Key []byte

	PrevKey []byte
	NextKey []byte
}

func (q *queue) Put(data []byte) error {
	q.mu.Lock()
	q.size++
	if q.head == nil {
		q.head = data
		q.tail = data
		q.mu.Unlock()
		return q.db.Update(func(txn *badger.Txn) error {
			return putNode(&node{Key: data}, txn)
		})
	}
	next := q.tail
	q.tail = data
	q.mu.Unlock()
	n := node{
		Key:     data,
		PrevKey: nil,
		NextKey: next,
	}
	err := q.db.Update(func(txn *badger.Txn) error {
		tail := &node{Key: next}
		err := getNode(tail, txn)
		if err != nil {
			return err
		}
		tail.PrevKey = data
		err = putNode(tail, txn)
		if err != nil {
			return err
		}
		return putNode(&n, txn)
	})
	return err
}

func (q *queue) Pop() ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	node := &node{Key: q.head}
	err := q.db.Update(func(txn *badger.Txn) error {
		err := getNode(node, txn)
		if err != nil {
			return err
		}
		return txn.Delete(node.Key)
	})
	if err != nil {
		return nil, err
	}
	// q.mu.Lock()
	q.head = node.PrevKey
	q.size--
	// q.mu.Unlock()
	return node.Key, nil
}

func (q *queue) Size() int { return q.size }

func getNode(n *node, txn *badger.Txn) error {
	item, err := txn.Get(n.Key)
	if err != nil {
		return err
	}
	return item.Value(func(val []byte) error {
		return json.Unmarshal(val, n)
	})
}

func putNode(n *node, txn *badger.Txn) error {
	raw := marshal(n)
	return txn.Set(n.Key, raw)
}

func marshal(n *node) []byte {
	raw, err := json.Marshal(n)
	if err != nil {
		panic(err)
	}
	return raw
}

func unmarshal(b []byte) *node {
	n := new(node)
	if err := json.Unmarshal(b, n); err != nil {
		panic(err)
	}
	return n
}
