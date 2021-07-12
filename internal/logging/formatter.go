package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
)

// PrefixedFormatter is a logging text formatter that logs with a prefix
type PrefixedFormatter struct {
	Prefix           string
	TimeFormat       string
	MaxMessageLength int
	NoColor          bool

	mu          sync.Mutex
	maxFieldLen map[string]int
	maxMsgLen   int
	format      string

	init sync.Once
}

func NewPrefixedFormatter(prefix, timeFormat string) *PrefixedFormatter {
	if timeFormat == "" {
		timeFormat = time.RFC3339
	}
	return &PrefixedFormatter{
		Prefix:           prefix,
		TimeFormat:       timeFormat,
		maxFieldLen:      make(map[string]int),
		MaxMessageLength: 250,
		format:           "",
	}
}

// Format using the prefixed formatter
func (pf *PrefixedFormatter) Format(e *logrus.Entry) ([]byte, error) {
	var col color.Attribute
	switch e.Level {
	case logrus.PanicLevel, logrus.ErrorLevel:
		col = color.FgRed
	case logrus.WarnLevel:
		col = color.FgYellow
	case logrus.InfoLevel:
		col = color.FgCyan
	case logrus.DebugLevel, logrus.TraceLevel:
		col = color.FgWhite
	default:
		return nil, errors.New("unknown logging level")
	}
	var keys []string
	for k := range e.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	levelStr := strings.ToUpper(e.Level.String())
	var b bytes.Buffer

	msglen := len(e.Message)
	pf.mu.Lock()
	if msglen > pf.MaxMessageLength {
		msglen = pf.MaxMessageLength
	}
	if pf.maxMsgLen < msglen {
		pf.maxMsgLen = msglen
	}
	pf.mu.Unlock()

	pf.init.Do(func() {
		if isTerm(e.Logger.Out) {
			pf.NoColor = true
		}
		if pf.TimeFormat == "" {
			pf.TimeFormat = time.RFC3339
		}
		if pf.NoColor {
			if pf.Prefix == "" {
				pf.format = "[%[1]s] %-7[3]s %[4]s%[5]s"
			} else {
				pf.format = "[%[1]s] %-7[3]s %[4]s: %[5]s"
			}
		} else {
			if pf.Prefix == "" {
				pf.format = "\x1b[90m[%s]\x1b[0m \x1b[%dm%-7s\x1b[0m %s%s"
			} else {
				pf.format = "\x1b[90m[%s]\x1b[0m \x1b[%dm%-7s\x1b[0m %s: %s"
			}
		}
	})

	pf.mu.Lock()
	fmt.Fprintf(&b, pf.format,
		e.Time.Format(pf.TimeFormat), col, levelStr, pf.Prefix, e.Message)
	fmt.Fprint(&b, strings.Repeat(" ", pf.maxMsgLen-msglen))
	pf.mu.Unlock()

	for _, k := range keys {
		val := e.Data[k]
		s, ok := val.(string)
		if !ok {
			s = fmt.Sprint(val)
		}
		fmt.Fprintf(&b, " \x1b[%dm%s\x1b[0m=", col, k)
		if !needsQuotes(s) {
			b.WriteString(s)
		} else {
			b.WriteString(fmt.Sprintf("%q", s))
		}
	}

	// check the last character for a \n
	if b.Bytes()[b.Len()-1] != '\n' {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

func isTerm(w io.Writer) bool {
	switch v := w.(type) {
	case *os.File:
		return terminal.IsTerminal(int(v.Fd()))
	default:
		return false
	}
}

// SilentFormatter is a logrus formatter that does nothing
type SilentFormatter struct{}

// Format does nothing
func (sf *SilentFormatter) Format(*logrus.Entry) ([]byte, error) {
	return nil, nil
}

func needsQuotes(s string) bool {
	if len(s) == 0 {
		return true
	}
	for _, c := range s {
		if !isChar(c) {
			return true
		}
	}
	return false
}

func isChar(c rune) bool {
	return c >= '!' && c <= '~'
}
