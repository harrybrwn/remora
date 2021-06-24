package badgerstore

import "github.com/dgraph-io/badger/v3"

func New(db *badger.DB) *badgerstore {
	return &badgerstore{db}
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

func (s *badgerstore) Close() error {
	return s.db.Close()
}
