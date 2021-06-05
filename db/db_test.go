package db

import (
	"os"
	"testing"
	"time"
)

func TestFindServiceFile(t *testing.T) {
	v := "this-is-a-test" + time.Now().String()
	os.Setenv("PGSERVICE", v)
}

func TestExecPGConfig(t *testing.T) {
	out, err := execPGConfig("--sysconfdir")
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("no output")
	}
}
