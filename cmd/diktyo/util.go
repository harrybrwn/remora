package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/harrybrwn/diktyo/internal"
	"github.com/sirupsen/logrus"
)

func isTooManyOpenFiles(err error) bool {
	e := internal.UnwrapAll(err)
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

func ipAddrs(host string) (v4, v6 interface{}, err error) {
	var addrs []net.IP
	addrs, err = net.LookupIP(host)
	if err != nil {
		return
	}
	for _, addr := range addrs {
		if addr.To4() != nil {
			v4 = addr.String()
		} else if addr.To16() != nil {
			v6 = addr.String()
		}
	}
	if v4 == "" {
		v4 = nil
	}
	if v6 == "" {
		v6 = nil
	}
	return
}

func memoryLogs(mem *runtime.MemStats) logrus.Fields {
	lastGC := time.Since(time.Unix(0, int64(mem.LastGC)))
	return logrus.Fields{
		"heap":   fmt.Sprintf("%03.02fmb", toMB(mem.HeapAlloc)),
		"sys":    fmt.Sprintf("%03.02fmb", toMB(mem.Sys)),
		"frees":  mem.Frees,
		"GCs":    mem.NumGC,
		"lastGC": lastGC.Truncate(time.Millisecond),
	}
}

func isNoSuchHost(err error) bool {
	switch v := err.(type) {
	case *url.Error:
		urlerr, ok := err.(*url.Error)
		if !ok {
			return false
		}
		operr, ok := urlerr.Err.(*net.OpError)
		if !ok {
			return false
		}
		return isNoSuchHost(operr)
	case *net.DNSError:
		return v.IsNotFound
	}
	return false
}

func validURLScheme(scheme string) bool {
	switch scheme {
	case
		"ftp",          // file transfer protocol
		"irc",          // IRC chat
		"mailto",       // email
		"tel",          // telephone
		"sms",          // text messaging
		"fb-messenger", // facebook messenger
		"waze",         // waze maps app
		"whatsapp",     // whatsapp messenger app
		"javascript",
		"":
		return false
	case "http", "https": // TODO add support for other protocols later
		return true
	}
	return false
}
