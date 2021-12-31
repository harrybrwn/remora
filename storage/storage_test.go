package storage

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/dgraph-io/badger/v3"
	"github.com/go-redis/redis/v8"
)

func testSets(t *testing.T) ([]URLSet, func()) {
	opts := badger.DefaultOptions("")
	opts.InMemory = true
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	sets := []URLSet{
		NewBadgerURLSet(db),
		NewInMemoryURLSet(),
	}
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx := context.Background()
	if err = client.Ping(ctx).Err(); err == nil {
		log.Println("adding redis to tests")
		sets = append(sets, NewRedisURLSet(client))
	}
	return sets, func() { db.Close(); client.Close() }
}

func TestURLSet(t *testing.T) {
	var (
		err error
		ctx = context.Background()
	)
	sets, cleanup := testSets(t)
	defer cleanup()
	for _, s := range sets {
		u := &url.URL{
			Scheme: "https",
			Host:   "www.google.com",
			Path:   "/",
		}
		err = s.Put(ctx, u)
		if err != nil {
			t.Error(err)
			continue
		}
		ok := s.Has(ctx, u)
		if !ok {
			t.Error("expected url to be in set")
		}
		u.Fragment = "#other-part-of-same-page"
		ok = s.Has(ctx, u)
		if !ok {
			t.Error("should return true even if url has a fragment")
		}
		err = s.Put(ctx, &url.URL{Scheme: "https", Host: "www.google.com", Path: "/search"})
		if err != nil {
			t.Error(err)
			continue
		}
		err = s.Put(ctx, &url.URL{Scheme: "https", Host: "www.google.com", Path: "/maps"})
		if err != nil {
			t.Error(err)
			continue
		}
		err = s.Put(ctx, &url.URL{Scheme: "https", Host: "www.google.com", Path: "/other_stuff"})
		if err != nil {
			t.Error(err)
			continue
		}
		oks := s.HasMulti(ctx, []*url.URL{
			{Scheme: "https", Host: "www.google.com", Path: "/search"},
			{Scheme: "https", Host: "www.google.com", Path: "/maps"},
			{Scheme: "https", Host: "www.google.com", Path: "/maps", Fragment: "#fragment"},
			{Scheme: "https", Host: "www.google.com", Path: "/other_stuff"},
			{Scheme: "https", Host: "www.google.com", Path: "/not-here"},
		})
		for i := 0; i < 4; i++ {
			if !oks[i] {
				t.Errorf("expected url %d to be in the set", i)
			}
		}
		if oks[4] {
			t.Error("url 5 should not be in the set")
		}
	}
}

func TestURLSet_Err(t *testing.T) {
	ctx := context.Background()
	sets, cleanup := testSets(t)
	defer cleanup()
	for _, s := range sets {
		s.Put(ctx, &url.URL{})
		s.Has(ctx, &url.URL{})
		s.HasMulti(ctx, nil)
	}
}

// Should be run with -race
func TestURLSetRace(t *testing.T) {
	var (
		wg  sync.WaitGroup
		ctx = context.Background()
	)
	sets, cleanup := testSets(t)
	defer cleanup()
	for _, s := range sets {
		wg.Add(1)
		go func(s URLSet) {
			defer wg.Done()
			// Doing my best to break anything and everything
			for i := 0; i < 5_000; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := s.Put(ctx, &url.URL{
						Scheme: "https",
						Host:   "www.google.com",
						Path:   "/",
					})
					if err != nil {
						t.Error(err)
					}
					ok := s.Has(ctx, &url.URL{
						Scheme: "https",
						Host:   "www.google.com",
						Path:   "/",
					})
					if !ok {
						t.Error("should have url in set")
					}
				}()
			}
		}(s)

	}
	wg.Wait()
}
