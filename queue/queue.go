package queue

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"sync"

	"github.com/dgraph-io/badger/v3"
)

type Queue interface {
	Put([]byte) error
	Pop() ([]byte, error)

	// PutKey gives the option to specify which key
	// is used for the backend storage.
	PutKey(key, val []byte) error

	Size() int
}

func Open(opts badger.Options) (Queue, error) {
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return New(db), nil
}

func New(db *badger.DB) Queue {
	q := &queue{db: db}
	q.empty = sync.NewCond(&q.mu)
	// q.sig = make(chan struct{})
	return q
}

type queue struct {
	db *badger.DB

	mu    sync.Mutex
	empty *sync.Cond
	// sig   chan struct{}

	head, tail []byte
	size       int
}

type node struct{ Key, Val, Next []byte }

func (q *queue) waitSig() {
	for q.size == 0 {
		q.empty.Wait()
	}
	// <-q.sig
}

func (q *queue) signal() {
	q.empty.Signal()
	// go func() { q.sig <- struct{}{} }()
}

func (q *queue) Put(data []byte) error {
	return q.PutKey(data, nil)
}

// PutKey will put a new value at the back of the queue by storing
// some value using the given key in the queue backend.
func (q *queue) PutKey(key, value []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.size++
	if q.head == nil {
		q.head = key
		q.tail = key
		q.signal()
		return q.db.Update(func(txn *badger.Txn) error {
			return putNode(&node{Key: key, Val: value}, txn)
		})
	}

	next := q.tail
	q.tail = key
	n := node{
		Key:  key,
		Val:  value,
		Next: nil,
	}
	err := q.db.Update(func(txn *badger.Txn) error {
		tail := &node{Key: next}
		err := getNode(tail, txn)
		if err != nil {
			return err
		}
		tail.Next = key
		err = putNode(tail, txn)
		if err != nil {
			return err
		}
		return putNode(&n, txn)
	})
	if err != nil {
		// put failed, don't signal
		return err
	}
	q.signal()
	return nil
}

func (q *queue) Pop() ([]byte, error) {
	q.mu.Lock()
	q.waitSig() // halt if the stack is empty, continue on put event
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
	q.head = node.Next
	q.size--
	if node.Val == nil {
		return node.Key, nil
	}
	return node.Val, nil
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
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return txn.Set(n.Key, raw)
}

func getNodeGob(n *node, txn *badger.Txn) error {
	var buf bytes.Buffer
	item, err := txn.Get(n.Key)
	if err != nil {
		return err
	}
	return item.Value(func(val []byte) error {
		_, err = buf.Write(val)
		if err != nil {
			return err
		}
		return gob.NewDecoder(&buf).Decode(n)
	})
}

func putNodeGob(n *node, txn *badger.Txn) error {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(n)
	if err != nil {
		return err
	}
	return txn.Set(n.Key, buf.Bytes())
}
