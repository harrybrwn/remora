package logging

import (
	"io"

	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
)

// TODO write a Recover function that recovers from a panic, grabs
// the stack frame to write to the logger and then panics again.

// Logger is a basic logger
type Logger interface {
	Errorf(string, ...interface{})
	Warningf(string, ...interface{})
	Infof(string, ...interface{})
	Debugf(string, ...interface{})
}

// Copy returns a copy of the logrus standar logger
func Copy() *logrus.Logger {
	l := logrus.StandardLogger()
	return &logrus.Logger{
		Out:          l.Out,
		Hooks:        l.Hooks,
		Formatter:    l.Formatter,
		ReportCaller: l.ReportCaller,
		Level:        l.Level,
		ExitFunc:     l.ExitFunc,
	}
}

// NewLogFileHook will create a logrus hook that logs to a log file
// for all logging levels
func NewLogFileHook(file io.Writer, formatter log.Formatter) log.Hook {
	return &logfilehook{
		file:      file,
		level:     log.TraceLevel,
		formatter: formatter,
	}
}

type logfilehook struct {
	file      io.Writer
	level     log.Level
	formatter log.Formatter
}

func (lf *logfilehook) Levels() []log.Level {
	return log.AllLevels //[:lf.level+1]
}

func (lf *logfilehook) Fire(e *log.Entry) error {
	// var entry log.Entry = *e
	// entry.Level = log.TraceLevel
	b, err := lf.formatter.Format(e)
	if err != nil {
		return err
	}
	_, err = lf.file.Write(b)
	return err
}

// NewMaybeLogger creates a new logger from an existing
// one that can safely be nil without panicing
func NewMaybeLogger(log Logger) Logger {
	return &MaybeLogger{log}
}

// MaybeLogger is a logger that can hold a nil logger internally
type MaybeLogger struct {
	log Logger
}

// Errorf reports an error
func (ml *MaybeLogger) Errorf(s string, v ...interface{}) {
	if ml.log != nil {
		ml.log.Errorf(s, v...)
	}
}

// Warningf reports a warning
func (ml *MaybeLogger) Warningf(s string, v ...interface{}) {
	if ml.log != nil {
		ml.log.Warningf(s, v...)
	}
}

// Infof reports info
func (ml *MaybeLogger) Infof(s string, v ...interface{}) {
	if ml.log != nil {
		ml.log.Infof(s, v...)
	}
}

// Debugf reports debugging info
func (ml *MaybeLogger) Debugf(s string, v ...interface{}) {
	if ml.log != nil {
		ml.log.Debugf(s, v...)
	}
}

// Hook is a generic logging hook... Stolen from the logrus
type Hook struct {
	Writer    io.Writer
	LogLevels []log.Level
}

// Fire will be called when some logging function is called with current hook
// It will format log entry to string and write it to appropriate writer
func (hook *Hook) Fire(entry *log.Entry) error {
	line, err := entry.Bytes()
	if err != nil {
		return err
	}
	_, err = hook.Writer.Write(line)
	return err
}

// Levels define on which log levels this hook would trigger
func (hook *Hook) Levels() []log.Level {
	return hook.LogLevels
}

// Discard an logs sent to this logger
var Discard Logger = &discardLogger{}

// This is the io.Discard of loggers
type discardLogger struct{}

// Errorf does nothing
func (*discardLogger) Errorf(string, ...interface{}) {}

// Warningf does nothing
func (*discardLogger) Warningf(string, ...interface{}) {}

// Infof does nothing
func (*discardLogger) Infof(string, ...interface{}) {}

// Debugf does nothing
func (*discardLogger) Debugf(string, ...interface{}) {}
