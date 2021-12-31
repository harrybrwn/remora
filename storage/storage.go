package storage

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v3"
	redis "github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

var log *logrus.Logger = logrus.New()

type Store interface {
	Get([]byte) ([]byte, error)
	Set([]byte, []byte) error
	Has([]byte) bool
}

type URLSet interface {
	Put(context.Context, *url.URL) error
	Has(context.Context, *url.URL) bool
	HasMulti(context.Context, []*url.URL) []bool
}

type Redis interface {
	MGet(context.Context, ...string) *redis.SliceCmd
	Get(context.Context, string) *redis.StringCmd
	Set(context.Context, string, interface{}, time.Duration) *redis.StatusCmd
}

func SetLogger(l *logrus.Logger) { log = l }

func NewRedisURLSet(client Redis) URLSet   { return &redisURLSet{client} }
func NewBadgerURLSet(db *badger.DB) URLSet { return &visitedSet{db} }
func NewInMemoryURLSet() URLSet            { return &inMemoryURLSet{m: make(map[string]struct{})} }

type redisURLSet struct {
	client Redis
}

func (set *redisURLSet) Has(ctx context.Context, u *url.URL) bool {
	var l = *u
	stripURL(&l)
	err := set.client.Get(ctx, l.String()).Err()
	if err != nil && err != redis.Nil {
		log.WithError(err).Warn("failed to get key from redis")
	}
	return err != redis.Nil
}

func (set *redisURLSet) Put(ctx context.Context, u *url.URL) error {
	var l = *u
	stripURL(&l)
	return set.client.Set(ctx, l.String(), 1, 0).Err()
}

func (set *redisURLSet) HasMulti(ctx context.Context, urls []*url.URL) []bool {
	ok := make([]bool, len(urls))
	keys := urlKeys(urls)
	result, err := set.client.MGet(ctx, keys...).Result()
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
		stripURL(&u)
		s[i] = u.String()
	}
	return s
}

type visitedSet struct {
	db *badger.DB
}

func (vs *visitedSet) Has(_ context.Context, u *url.URL) bool {
	var l = *u
	ok := false
	stripURL(&l)

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

func (vs *visitedSet) HasMulti(_ context.Context, urls []*url.URL) []bool {
	ok := make([]bool, len(urls))
	keys := make([][]byte, len(urls))
	var u url.URL
	for i, l := range urls {
		u = *l
		stripURL(&u)
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

func (vs *visitedSet) Put(_ context.Context, u *url.URL) error {
	var l = *u
	stripURL(&l)

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

type inMemoryURLSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func stripURL(u *url.URL) {
	u.Fragment = ""
	u.RawFragment = ""
}

func (s *inMemoryURLSet) Has(_ context.Context, u *url.URL) bool {
	var l = *u
	stripURL(&l)
	s.mu.Lock()
	_, ok := s.m[l.String()]
	s.mu.Unlock()
	return ok
}

func (s *inMemoryURLSet) Put(_ context.Context, u *url.URL) error {
	var l = *u
	stripURL(&l)
	s.mu.Lock()
	s.m[l.String()] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *inMemoryURLSet) HasMulti(_ context.Context, urls []*url.URL) []bool {
	var l url.URL
	ok := make([]bool, len(urls))
	s.mu.Lock()
	for i, url := range urls {
		l = *url
		stripURL(&l)
		_, ok[i] = s.m[l.String()]
	}
	s.mu.Unlock()
	return ok
}

func NewBadgerStore(db *badger.DB) *badgerstore {
	return &badgerstore{db}
}

func NewRedisStore(client *redis.Client) *redisstore {
	return &redisstore{client}
}

type badgerstore struct {
	db *badger.DB
}

func (s *badgerstore) Get(k []byte) ([]byte, error) {
	var res []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(k)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			copy(res, val)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *badgerstore) Set(k, v []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(k, v)
	})
}

func (s *badgerstore) Has(k []byte) bool {
	var res bool
	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(k)
		if err == nil {
			res = true
		}
		return nil
	})
	if err != nil {
		res = false
	}
	return res
}

type redisstore struct {
	client *redis.Client
}

func (rs *redisstore) Get(k []byte) ([]byte, error) {
	res, err := rs.client.Get(context.TODO(), string(k)).Result()
	if err != nil {
		return nil, err
	}
	return []byte(res), err
}

func (rs *redisstore) Set(k, v []byte) error {
	return rs.client.Set(context.TODO(), string(k), v, 0).Err()
}

func (rs *redisstore) Has(k []byte) bool {
	err := rs.client.Get(context.TODO(), string(k)).Err()
	return err != redis.Nil
}
