package queue

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v3"
)

func Test(t *testing.T) {
	t.Skip()
	var (
		mu sync.RWMutex
	)
	reader := func(i int) {
		mu.RLock()
		fmt.Printf("reader %d acquire\n", i)
		time.Sleep(time.Second)
		fmt.Printf("reader %d release\n", i)
		mu.RUnlock()
	}
	writer := func(i int) {
		mu.Lock()
		fmt.Printf("writer %d acquire\n", i)
		time.Sleep(time.Second * 2)
		fmt.Printf("writer %d release\n", i)
		mu.Unlock()
	}

	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			go reader(i)
		} else {
			go writer(i)
		}
	}
	<-time.After(time.Second * 10)

}

func q(t *testing.T) *queue {
	dir, err := os.MkdirTemp("", "disk-queue-test-*")
	if err != nil {
		t.Fatal(err)
	}
	opts := badger.DefaultOptions(dir)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatal(err)
		return nil
	}
	return New(db, []byte{}).(*queue)
}

func rm(q *queue) error {
	if err := q.Close(); err != nil {
		fmt.Println(err)
	}
	return os.RemoveAll(q.db.Opts().Dir)
}

func TestOnePushPop(t *testing.T) {
	q := q(t)
	q.prefix = []byte("test_")
	defer rm(q)
	for _, s := range []string{
		"one", "two", "three", "four",
	} {
		err := q.Put([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		b, err := q.Pop()
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != s {
			t.Errorf("got %q, wanted %q", b, s)
		}
	}
}

func TestMultiPushPop(t *testing.T) {
	var (
		err error
		q   = q(t)
	)
	defer rm(q)
	keys := []string{"one", "two", "three"}
	for _, k := range keys {
		if err = q.PutKey([]byte(k), []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(q.head, []byte("one")) {
		t.Error("wanted one")
	}
	if !bytes.Equal(q.tail, []byte("three")) {
		t.Error("wanted three")
	}

	// Check that all the keys are actually there
	for _, k := range keys {
		err = q.db.View(func(txn *badger.Txn) error {
			if _, err = txn.Get([]byte(k)); err != nil {
				t.Error(err)
				return err
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}

	for i := range keys {
		res, err := q.Pop()
		if err != nil {
			t.Error(err)
			continue
		}
		if string(res) != keys[i] {
			t.Errorf("got %s, wanted %s", res, keys[i])
		}
	}
}

func TestConncurentReaders(t *testing.T) {
	var (
		err error
		q   = q(t)
	)
	defer rm(q)
	for i := byte(0); i < 200; i++ {
		err = q.Put([]byte{i})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < (200 / n); j++ {
				b, err := q.Pop()
				if err != nil {
					t.Error(err)
				}
				if len(b) == 0 {
					t.Error("zero length result")
				}
			}
		}()
	}
	wg.Wait()
}

func TestConncurentRW(t *testing.T) {
	q := q(t)
	q.prefix = nil
	defer rm(q)
	d := time.Millisecond * 10
	ok := make(chan bool)

	go func() {
		<-time.After(d * 2)
		ok <- false
	}()
	go func() {
		time.Sleep(d)
		err := q.Put([]byte("one"))
		if err != nil {
			t.Error(err)
		}
	}()

	go func() {
		v, err := q.Pop()
		if err != nil {
			t.Error(err)
		}
		if !bytes.Equal(v, []byte("one")) {
			t.Errorf("got %q, want %q", v, "one")
		}
		ok <- true
	}()

	if !<-ok {
		t.Fatal("time limit expired, no value popped")
	}
}

func TestLargeRW(t *testing.T) {
	type index_t = uint32
	var (
		wg sync.WaitGroup
		n  = index_t(0xffff)
	)
	q := q(t)
	defer q.Close()
	defer rm(q)

	key := func(x index_t) []byte {
		return []byte{byte(x >> 8), byte(x & 0x00ff)}
	}

	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := index_t(0); i < n; i++ {
			v, err := q.Pop()
			if err != nil {
				t.Error(err)
				continue
			}
			if len(v) == 0 {
				t.Error("zero length result")
				continue
			}
			if !bytes.Equal(v, key(i)) {
				// if !bytes.Equal(v, []byte{byte(i >> 8), byte(i & 0x00ff)}) {
				t.Errorf("got %v, want %v", v, i)
				continue
			}
		}
	}()

	// make sure pop is called first to simulate real world conditions
	time.Sleep(time.Millisecond * 10)

	go func() {
		defer wg.Done()
		for i := index_t(0); i < n; i++ {
			err := q.PutKey(key(i), key(i))
			if err != nil {
				t.Error(err)
			}
		}
	}()

	wg.Wait()
}

func TestCloseQueue(t *testing.T) {
	var (
		wg sync.WaitGroup
		q  = q(t)
	)
	defer rm(q)
	for _, e := range []error{
		q.Put([]byte{1}),
		q.Put([]byte{5}),
		eatb(q.Pop()),
	} {
		if e != nil {
			t.Fatal(e)
		}
	}

	err := q.Close()
	if err != nil {
		t.Fatal(err)
	}

	check := func(e error) {
		if e != ErrQueueClosed {
			t.Error("queue should be closed")
		}
	}
	wg.Add(2)
	n := 100
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			check(q.Put([]byte{byte(i)}))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, err := q.Pop()
			check(err)
		}
	}()
	wg.Wait()
}

func randomkeys(n int) [][]byte {
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = make([]byte, 32)
		_, err := rand.Read(keys[i])
		if err != nil {
			panic(err)
		}
	}
	return keys
}

func TestRandomKeys(t *testing.T) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		q    = q(t)
		keys = randomkeys(512 * 2)
		l    = len(keys)
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < l; i++ {
			for _, fn := range []func() ([]byte, error){
				q.Peek,
				q.Pop,
			} {
				res, err := fn()
				if err != nil {
					t.Error(err)
					continue
				}
				mu.Lock()
				if !bytes.Equal(res, keys[i]) {
					t.Errorf("expected %s, got %s", keys[i], res)
				}
				mu.Unlock()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < l; i++ {
			mu.Lock()
			k := keys[i]
			mu.Unlock()
			err := q.Put(k)
			if err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
}

func eatb(_ []byte, e error) error { return e }
