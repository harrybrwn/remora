package storage

import (
	"net/url"

	"github.com/dgraph-io/badger/v3"
	"github.com/go-redis/redis"
)

type Store interface {
	Get([]byte) ([]byte, error)
	Set([]byte, []byte) error
	Has([]byte) bool
	Close() error
}

type URLSet interface {
	Put(*url.URL) error
	Has(*url.URL) bool
	HasMulti([]*url.URL) []bool
}

func NewRedisURLSet(client *redis.Client) URLSet {
	return &redisURLSet{client}
}

type redisURLSet struct {
	client *redis.Client
}

func (set *redisURLSet) Has(u *url.URL) bool {
	var l = *u
	l.Fragment = ""
	l.RawFragment = ""
	err := set.client.Get(l.String()).Err()
	return err != redis.Nil
}

func (set *redisURLSet) Put(u *url.URL) error {
	var l = *u
	l.Fragment = ""
	l.RawFragment = ""
	return set.client.Set(l.String(), 1, 0).Err()
}

func (set *redisURLSet) HasMulti(urls []*url.URL) []bool {
	ok := make([]bool, len(urls))
	keys := urlKeys(urls)
	result, err := set.client.MGet(keys...).Result()
	if err != nil {
		return ok
	}
	for i, r := range result {
		ok[i] = r != nil
	}
	return ok
}

func urlKeys(links []*url.URL) []string {
	s := make([]string, len(links))
	var u url.URL
	for i, l := range links {
		u = *l
		u.Fragment = ""
		u.RawFragment = ""
		s[i] = u.String()
	}
	return s
}

func NewBadgerURLSet(db *badger.DB) *visitedSet {
	return &visitedSet{db}
}

type visitedSet struct {
	db *badger.DB
}

func (vs *visitedSet) Has(u *url.URL) bool {
	var l = *u
	ok := false
	l.Fragment = ""

	key := urlKey(&l)
	err := vs.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if item.IsDeletedOrExpired() {
			return nil
		}
		ok = true
		return nil
	})
	if err != nil {
		return false
	}
	return ok
}

func (vs *visitedSet) HasMulti(urls []*url.URL) []bool {
	ok := make([]bool, len(urls))
	keys := make([][]byte, len(urls))
	var u url.URL
	for i, l := range urls {
		u = *l
		u.Fragment = ""
		u.RawFragment = ""
		keys[i] = urlKey(&u)
	}
	vs.db.View(func(txn *badger.Txn) error {
		for i, k := range keys {
			item, err := txn.Get(k)
			if err == badger.ErrKeyNotFound {
				continue
			}
			if item.IsDeletedOrExpired() {
				continue
			}
			ok[i] = true
		}
		return nil
	})
	return ok
}

func (vs *visitedSet) Put(u *url.URL) error {
	var l = *u
	l.Fragment = ""

	key := urlKey(&l)
	return vs.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, []byte{1})
	})
}

func urlKey(u *url.URL) []byte {
	s := u.String()
	key := make([]byte, 8, len(s)+8)
	copy(key[:], []byte("visited_"))
	return append(key, []byte(s)...)
}
