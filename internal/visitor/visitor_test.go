package visitor

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-redis/redis/v8"
	"github.com/harrybrwn/remora/web"
	"github.com/sirupsen/logrus"
)

var logs bytes.Buffer

func init() {
	log.SetLevel(logrus.TraceLevel) // make sure loggins is happening
	log.SetOutput(&logs)            // but logging is silent
}

type redisMock struct {
}

func (rm *redisMock) MGet(context.Context, ...string) *redis.SliceCmd { return &redis.SliceCmd{} }
func (rm *redisMock) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("testvalue", redis.Nil) // always returns not found
}
func (rm *redisMock) Set(context.Context, string, interface{}, time.Duration) *redis.StatusCmd {
	return &redis.StatusCmd{}
}

func TestVisit(t *testing.T) {
	t.Skip()
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
	)
	v, err := New(db, &redisMock{})
	if err != nil {
		t.Fatal(err)
	}
	AddHost(v, "en.wikipedia.org")
	links := []string{
		"https://en.wikipedia.org/wiki/Help:Introduction",
	}
	page.Links = urls(links...)
	page.Response = &http.Response{StatusCode: 200, Body: http.NoBody}
	page.Words = []string{"one", "two", "three"}
	page.RedirectedFrom = page.URL
	page.Redirected = false
	page.ContentType = "text/html"
	page.ResponseTime = time.Millisecond * 50

	_ = pageid
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM edge WHERE parent_id = $1").WithArgs(pageid).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectExec(
		"INSERT INTO edge(parent_id,child_id,child)"+
			"VALUES ($1,$2,$3) "+
			"ON CONFLICT(parent_id,child_id)DO NOTHING",
	).WithArgs(
		pageid,
		getID(links[0]),
		links[0],
	).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectExec(insertPageSQL).WillReturnResult(driver.ResultNoRows)
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

func Test(t *testing.T) {
	buf, vals := insertEdgesQuery([]byte{1, 2, 3}, "https://en.wikipedia.org/", []string{
		"https://en.wikipedia.org/wiki/",
		"https://en.wikipedia.org/wiki/Main_Page",
		"https://en.wikipedia.org/wiki/Wikipedia",
		"https://en.wikipedia.org/wiki/Special:Random",
		"https://meta.wikimedia.org/wiki/Main_Page",
		"https://en.wikipedia.org/wiki/Portal:Science",
		"https://en.wikipedia.org/wiki/Wikipedia:Featured_lists",
		"https://en.wikipedia.org/wiki/Wikipedia:Help_desk",
		"https://en.wikipedia.org/wiki/Wikipedia:Teahouse",
		"https://en.wikipedia.org/wiki/Help:Contents",
		"https://stats.wikimedia.org/#/en.wikipedia.org",
	})
	fmt.Println(len(vals))
	fmt.Println(buf.String())
}

func urls(links ...string) []*url.URL {
	result := make([]*url.URL, len(links))
	for i, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			panic(fmt.Errorf("could not parse url: %w", err))
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
