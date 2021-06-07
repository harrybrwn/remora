package web

import (
	"errors"
	"net/url"

	"github.com/sirupsen/logrus"
)

var (
	ErrSkipURL = errors.New("skip url")

	log = logrus.StandardLogger()
)

type Visitor interface {
	// Filter is called after checking page depth
	// and after checking for a repeated URL.
	Filter(*PageRequest) error

	// Visit is called after a page is fetched.
	Visit(*Page)

	// LinkFound is called when a new link is
	// found and popped off of the main queue
	// and before any depth checking or repeat
	// checking.
	LinkFound(*url.URL)
}

func SetLogger(l *logrus.Logger) { log = l }
func GetLogger() *logrus.Logger  { return log }
