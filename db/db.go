package db

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/pkg/errors"
)

type Config struct {
	Host     string `yaml:"host" config:"host" default:"localhost"`
	Port     int    `yaml:"port" config:"port" default:"5432"`
	User     string `yaml:"user" config:"user" env:"POSTGRES_USER"`
	Password string `yaml:"password" config:"password" env:"POSTGRES_PASSWORD"`
	Name     string `yaml:"name" config:"name" env:"POSTGRES_DB"`
	SSL      string `yaml:"ssl" config:"ssl" default:"disable"`
}

func New(cfg *Config) (*sql.DB, error) {
	os.Unsetenv("PGSERVICEFILE") // lib/pq panics when this is set
	os.Unsetenv("PGSERVICE")

	db, err := sql.Open("postgres", cfg.dsn())
	if err != nil {
		return nil, errors.Wrap(err, "could not open postgres db")
	}
	err = db.Ping()
	if err != nil {
		db.Close()
		return nil, errors.Wrap(err, "could not ping postgres")
	}
	return db, nil
}

func WaitForNew(ctx context.Context, cfg *Config, ping time.Duration) (*sql.DB, error) {
	os.Unsetenv("PGSERVICEFILE") // lib/pq panics when this is set
	os.Unsetenv("PGSERVICE")
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
			return nil, driver.ErrBadConn
		case <-ticker.C:
			err = db.Ping()
			if err == nil {
				return db, nil
			}
		}
	}
}

func ServiceFileExists() {
}

func findServiceFiles() (string, error) {
	var filename string
	for _, key := range []string{
		"PGSERVICEFILE",
		"PGSERVICE",
	} {
		filename = os.Getenv(key)
		if filename != "" {
			break
		}
	}
	if exists(filename) {
		return filename, nil
	}

	var e1 error
	home, err := os.UserHomeDir()
	if err != nil {
		e1 = err
	}
	filename = filepath.Join(home, ".pg_service.conf")
	if exists(filename) {
		return filename, nil
	}
	return "", e1
}

func execPGConfig(args ...string) (string, error) {
	var (
		buf bytes.Buffer
		cmd = exec.Command("pg_config", args...)
	)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
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

func exists(filename string) bool {
	_, err := os.Stat(filename)
	return !os.IsNotExist(err)
}
