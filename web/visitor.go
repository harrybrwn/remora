package web

import (
	"context"
	"net/url"

	"github.com/pkg/errors"
)

var (
	// ErrSkipURL is returned from a Visitor method to
	// signal that the URL passed should be skipped.
	ErrSkipURL = errors.New("skip url")
)

type Visitor interface {
	// LinkFound is called when a new link is
	// found and popped off of the main queue
	// and before any depth checking or repeat
	// checking.
	LinkFound(*url.URL) error
	// Filter is called after checking page depth
	// and after checking for a repeated URL.
	Filter(*PageRequest, *url.URL) error
	// Visit is called after a page is fetched.
	Visit(context.Context, *Page)
}
