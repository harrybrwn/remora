package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/harrybrwn/remora/internal/region"
	_ "github.com/lib/pq"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Host     string `yaml:"host" config:"host" default:"localhost"`
	Port     int    `yaml:"port" config:"port" default:"5432"`
	User     string `yaml:"user" config:"user" env:"POSTGRES_USER"`
	Password string `yaml:"password" config:"password" env:"POSTGRES_PASSWORD"`
	Name     string `yaml:"name" config:"name" env:"POSTGRES_DB"`
	SSL      string `yaml:"ssl" config:"ssl" default:"disable"`

	Logger logrus.FieldLogger `yaml:"-" json:"-"`
}

func (c *Config) dsn() string {
	c.init()
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSL,
	)
}

func (c *Config) init() {
	if c.SSL == "" {
		c.SSL = "disable"
	}
}

func New(cfg *Config) (*sql.DB, error) {
	os.Unsetenv("PGSERVICEFILE") // lib/pq panics when this is set
	os.Unsetenv("PGSERVICE")
	db, err := sql.Open("postgres", cfg.dsn())
	if err != nil {
		return nil, errors.Wrap(err, "could not open postgres db")
	}
	return db, nil
}

func WaitForNew(ctx context.Context, cfg *Config, ping time.Duration) (*sql.DB, error) {
	os.Unsetenv("PGSERVICEFILE") // lib/pq panics when this is set
	os.Unsetenv("PGSERVICE")
	if cfg.Logger == nil {
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		cfg.Logger = logger
	}
	db, err := sql.Open("postgres", cfg.dsn())
	if err != nil {
		return nil, err
	}
	err = db.Ping()
	if err == nil {
		return db, nil
	}
	ticker := time.NewTicker(ping)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, err
		case <-ticker.C:
			cfg.Logger.WithField("time", time.Now()).Info("pinging database")
			err = db.Ping()
			if err == nil {
				return db, nil
			}
		}
	}
}

func NewWithTimeout(ctx context.Context, timeout time.Duration, cfg *Config) (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	db, err := WaitForNew(ctx, cfg, time.Second)
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to database")
	}
	return db, nil
}

type DB interface {
	io.Closer
	Queryable
	BeginTx(context.Context, ...TxOption) (Tx, error)
}

type Queryable interface {
	QueryContext(context.Context, string, ...interface{}) (Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

type Statement interface {
	io.Closer
	QueryContext(context.Context, ...interface{}) (Rows, error)
	ExecContext(context.Context, ...interface{}) (sql.Result, error)
}

type TxOption func(txo *sql.TxOptions)

type Tx interface {
	Queryable
	Commit() error
	Rollback() error
}

type Scanner interface {
	Scan(...interface{}) error
}

type Rows interface {
	Scanner
	io.Closer
	Next() bool
	Err() error
}

type dbOptions struct {
	name string
}

type DBOption func(o *dbOptions)

func WithName(name string) DBOption { return func(o *dbOptions) { o.name = name } }

func Wrap(database *sql.DB, opts ...DBOption) DB {
	var o dbOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.name == "" {
		o.name = "database/sql"
	}
	tracer := otel.Tracer(o.name)
	return &db{
		DB:     database,
		tracer: tracer,
		region: region.NewRegion(tracer, dbSystem),
	}
}

var (
	dbSystem = semconv.DBSystemPostgreSQL
	dbQuery  = semconv.DBStatementKey
)

type db struct {
	*sql.DB
	tracer trace.Tracer
	region *region.Region
}

var _ DB = (*db)(nil)

func (db *db) QueryContext(ctx context.Context, query string, v ...interface{}) (rs Rows, err error) {
	db.region.
		Attr(dbQuery.String(query)).
		Wrap(
			ctx, "query",
			func(ctx context.Context) ([]attribute.KeyValue, error) {
				rs, err = db.DB.QueryContext(ctx, query, v...)
				return nil, err
			},
		)
	return rs, err
}

func (db *db) ExecContext(ctx context.Context, query string, v ...interface{}) (res sql.Result, err error) {
	db.region.
		Attr(dbQuery.String(query)).
		Wrap(ctx, "exec", func(ctx context.Context) ([]attribute.KeyValue, error) {
			res, err = db.DB.ExecContext(ctx, query, v...)
			return nil, err
		})
	return res, err
}

func TxReadOnly(ro bool) TxOption {
	return func(txo *sql.TxOptions) { txo.ReadOnly = ro }
}

func TxIsolation(iso sql.IsolationLevel) TxOption {
	return func(txo *sql.TxOptions) { txo.Isolation = iso }
}

func (db *db) BeginTx(ctx context.Context, opts ...TxOption) (Tx, error) {
	var options sql.TxOptions
	for _, o := range opts {
		o(&options)
	}
	ctx, span := db.tracer.Start(
		ctx, "begin transaction",
		trace.WithLinks(trace.LinkFromContext(ctx)),
	)
	t, err := db.DB.BeginTx(ctx, &options)
	if err != nil {
		return nil, err
	}
	return &tx{
		tx:     t,
		span:   span,
		region: db.region.WithParent(span),
	}, nil
}

type tx struct {
	tx     *sql.Tx
	span   trace.Span
	region *region.Region
}

func (tx *tx) Commit() error   { return tx.end(tx.tx.Commit()) }
func (tx *tx) Rollback() error { return tx.end(tx.tx.Rollback()) }

func (tx *tx) end(e error) error {
	if e != nil {
		tx.span.RecordError(e)
		tx.span.SetStatus(codes.Error, e.Error())
	}
	tx.span.End()
	return e
}

func (tx *tx) QueryContext(ctx context.Context, query string, v ...interface{}) (rs Rows, err error) {
	tx.region.
		Attr(dbQuery.String(query)).
		Wrap(ctx, "query", func(ctx context.Context) ([]attribute.KeyValue, error) {
			rs, err = tx.tx.QueryContext(ctx, query, v...)
			return nil, err
		})
	return
}

func (tx *tx) ExecContext(ctx context.Context, query string, v ...interface{}) (res sql.Result, err error) {
	tx.region.
		Attr(dbQuery.String(query)).
		Wrap(ctx, "query", func(ctx context.Context) ([]attribute.KeyValue, error) {
			res, err = tx.tx.ExecContext(ctx, query, v...)
			return nil, err
		})
	return
}
