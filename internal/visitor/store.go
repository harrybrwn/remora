package visitor

import (
	"database/sql"

	"github.com/lib/pq"
)

type pageStore struct {
	db       *sql.DB
	pagestmt *sql.Stmt
	edgestmt *sql.Stmt
}

func newPageStore(db *sql.DB) (*pageStore, error) {
	pagestmt, err := db.Prepare(insertPageSQL)
	if err != nil {
		return nil, err
	}
	edgestmt, err := db.Prepare(pq.CopyIn("edge", "parent_id", "child_id", "child"))
	if err != nil {
		return nil, err
	}
	return &pageStore{
		db:       db,
		pagestmt: pagestmt,
		edgestmt: edgestmt,
	}, nil
}

func (ps *pageStore) Close() error {
	err := ps.pagestmt.Close()
	if err != nil {
		return err
	}
	err = ps.edgestmt.Close()
	if err != nil {
		return err
	}
	return nil
}
