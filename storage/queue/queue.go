package queue

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/dgraph-io/badger/v3"
)

var ErrQueueClosed = errors.New("queue closed")

type Queue interface {
	Put([]byte) error
	Pop() ([]byte, error)

	// PutKey gives the option to specify which key
	// is used for the backend storage.
	PutKey(key, val []byte) error

	Size() int64
	Close() error
}

func Open(opts badger.Options, prefix []byte) (Queue, error) {
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return New(db, prefix), nil
}

func New(db *badger.DB, prefix []byte) Queue {
	var mu sync.Mutex
	q := &queue{
		db:     db,
		prefix: prefix,
		empty:  sync.NewCond(&mu),
		closed: 0,
	}
	return q
}

type queue struct {
	db     *badger.DB
	prefix []byte

	empty  *sync.Cond
	closed uint32

	head, tail []byte
	size       int64
}

type node struct {
	Key   []byte
	Val   []byte
	Next  []byte
	Count int64
}

func (q *queue) Close() error {
	atomic.AddUint32(&q.closed, 1) // mark closed
	q.empty.Broadcast()            // wake everyone up
	return nil
}

func (q *queue) waitSig() {
	for q.size == 0 && q.closed == 0 {
		q.empty.Wait()
	}
}

func (q *queue) signal() {
	q.empty.Signal()
}

func (q *queue) isClosed() bool {
	return q.closed > 0
}

func (q *queue) Put(data []byte) error {
	return q.PutKey(data, nil)
}

// PutKey will put a new value at the back of the queue by storing
// some value using the given key in the queue backend.
func (q *queue) PutKey(key, value []byte) error {
	q.empty.L.Lock()
	defer q.empty.L.Unlock()
	if q.isClosed() {
		return ErrQueueClosed
	}
	var (
		err  error
		next []byte
		n    node
	)
	key = bytes.Join([][]byte{q.prefix, key}, nil)

	if q.head == nil || q.size == 0 {
		q.head = key
		q.tail = key
		err = q.db.Update(func(txn *badger.Txn) error {
			return putNode(&node{Key: key, Val: value}, txn)
		})
		if err != nil {
			return err
		}
		goto Success
	}

	next = q.tail
	q.tail = key
	n = node{
		Key:  key,
		Val:  value,
		Next: nil,
	}
	err = q.db.Update(func(txn *badger.Txn) error {
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
		return err
	}
Success:
	q.size++
	q.signal()
	return nil
}

func (q *queue) Pop() ([]byte, error) {
	q.empty.L.Lock()
	defer q.empty.L.Unlock()
	q.waitSig() // halt if the stack is empty, continue on put event
	if q.isClosed() {
		return nil, ErrQueueClosed
	}
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
		return bytes.TrimPrefix(node.Key, q.prefix), nil
	}
	return node.Val, nil
}

func (q *queue) Peek() ([]byte, error) {
	q.empty.L.Lock()
	defer q.empty.L.Unlock()
	q.waitSig() // halt if the stack is empty, continue on put event
	if q.isClosed() {
		return nil, ErrQueueClosed
	}
	node := &node{Key: q.head}
	err := q.db.Update(func(txn *badger.Txn) error {
		err := getNode(node, txn)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if node.Val == nil {
		return bytes.TrimPrefix(node.Key, q.prefix), nil
	}
	return node.Val, nil
}

func (q *queue) Size() int64 {
	q.empty.L.Lock()
	defer q.empty.L.Unlock()
	return q.size
}

func getNode(n *node, txn *badger.Txn) error {
	item, err := txn.Get(n.Key)
	if err != nil {
		return err
	}
	return item.Value(func(val []byte) error {
		return json.Unmarshal(val, n)
	})
	// return getNodeGob(n, txn)
}

func putNode(n *node, txn *badger.Txn) error {
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return txn.Set(n.Key, raw)
	// return putNodeGob(n, txn)
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
