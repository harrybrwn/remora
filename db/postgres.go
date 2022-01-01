package db

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

func exists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
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
