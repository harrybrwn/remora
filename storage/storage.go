package storage

import "net/url"

type Store interface {
	Get([]byte) ([]byte, error)
	Set([]byte, []byte) error
	Has([]byte) bool
	Close() error
}

type URLSet interface {
	Has(*url.URL) bool
	Put(*url.URL) error
}
