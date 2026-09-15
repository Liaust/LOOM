package knowledge

import "database/sql"

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) Store {
	return Store{db: db}
}

func (s Store) DB() *sql.DB {
	return s.db
}
