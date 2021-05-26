package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/sirupsen/logrus"
)

func unwrapAll(err error) error {
	var e error = err
	type unwrappable interface {
		Unwrap() error
	}
	unwrapper, ok := e.(unwrappable)
	for ok {
		e = unwrapper.Unwrap()
		unwrapper, ok = e.(unwrappable)
	}
	return e
}

func isTooManyOpenFiles(err error) bool {
	e := unwrapAll(err)
	errno, ok := e.(syscall.Errno)
	if !ok {
		return false
	}
	return errno == syscall.EMFILE || errno == syscall.ENFILE
}

func findNthFile(format string) (string, error) {
	var (
		nth  int
		name string
		err  error
	)
	for {
		if nth > 1000 {
			return "", errors.New("too many output files")
		}
		name = fmt.Sprintf(format, nth)
		_, err = os.Stat(name)
		if os.IsNotExist(err) || nth > 1_000 {
			return name, nil
		}
		nth++
	}
}

func setLoggerFile(l *logrus.Logger, name string) error {
	name, err := findNthFile(name + "_%d.log")
	if err != nil {
		return err
	}
	logfile, err := os.Create(name)
	if err != nil {
		return err
	}
	// Don't close file, it will be held for the entire program
	// duration anyways. If the caller really wants to close, then
	// they can use an type assertion for `interface { Close() error }`
	l.SetOutput(logfile)
	return nil
}

func loggerFile(name string) io.Writer {
	name, err := findNthFile(name + "_%d.log")
	if err != nil {
		return nil
	}
	logfile, err := os.Create(name)
	if err != nil {
		return nil
	}
	return logfile
}

func setErrorLogfile(l *logrus.Logger) error {
	return setLoggerFile(l, "diktyo_errors")
}
