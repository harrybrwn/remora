package queue

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/dgraph-io/badger/v3"
)

func TestOnePushPop(t *testing.T) {
	dir := testdir(t)
	defer os.RemoveAll(dir)
	q, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = q.Put([]byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := q.Pop()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "one" {
		t.Errorf("got %s, wanted %s", b, "one")
	}
}

func TestMultiPushPop(t *testing.T) {
	dir := testdir(t)
	defer os.RemoveAll(dir)
	q, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"one", "two", "three"}
	for _, k := range keys {
		if err = q.Put([]byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	queue := q.(*queue)
	if !bytes.Equal(queue.head, []byte("one")) {
		t.Error("wanted one")
	}
	if !bytes.Equal(queue.tail, []byte("three")) {
		t.Error("wanted three")
	}

	// Check that all the keys are actually there
	for _, k := range keys {
		err = queue.db.View(func(txn *badger.Txn) error {
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
	dir := testdir(t)
	defer os.RemoveAll(dir)
	q, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
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
}

func testdir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "disk-queue-test-*")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
