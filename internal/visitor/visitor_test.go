package visitor

import (
	"bytes"
	"context"
	"database/sql/driver"
	"hash/fnv"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
)

var logs bytes.Buffer

func init() {
	log.SetLevel(logrus.DebugLevel) // make sure loggins is happening
	log.SetOutput(&logs)            // but logging is silent
}

type redisMock struct{}

func (rm *redisMock) MGet(...string) *redis.SliceCmd { return &redis.SliceCmd{} }
func (rm *redisMock) Get(string) *redis.StringCmd    { return &redis.StringCmd{} }
func (rm *redisMock) Set(string, interface{}, time.Duration) *redis.StatusCmd {
	return &redis.StatusCmd{}
}

func TestVisit(t *testing.T) {
	db, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var (
		ctx    = context.Background()
		page   = web.NewPageFromString("https://en.wikipedia.org/wiki/Main_Page", 0)
		pageid = getID(page.URL.String())
		v      = New(db, &redisMock{})
	)
	AddHost(v, "en.wikipedia.org")
	links := []string{
		"https://en.wikipedia.org/wiki/Help:Introduction",
	}
	page.Links = urls(links...)

	mock.ExpectBegin()
	mock.ExpectExec(insertPageSQL).WillReturnResult(driver.ResultNoRows)
	mock.ExpectExec("DELETE FROM edge WHERE parent_id = $1").WithArgs(pageid).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectExec(
		"INSERT INTO edge (parent_id,child_id,child) "+
			"VALUES ($1,$2,$3) "+
			"ON CONFLICT (parent_id,child_id,child) DO NOTHING",
	).WithArgs(
		pageid,
		getID(links[0]),
		links[0],
	).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectCommit()

	v.Visit(ctx, page)
	if v.Visited != 1 {
		t.Errorf("expected visited == 1, got %d", v.Visited)
	}

	err = mock.ExpectationsWereMet()
	if err != nil {
		t.Fatal(err)
	}
}

func urls(links ...string) []*url.URL {
	result := make([]*url.URL, len(links))
	for i, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			panic(err)
		}
		result[i] = u
	}
	return result
}

func getID(s string) []byte {
	h := fnv.New128()
	io.WriteString(h, s)
	return h.Sum(nil)
}
