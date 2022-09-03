package schema

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

func Init(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user (
			id INTEGER NOT NULL,
			name TEXT NOT NULL,
			password TEXT NOT NULL,
			PRIMARY KEY (id),
			UNIQUE (name)
		);

		CREATE TABLE IF NOT EXISTS file (
			uuid CHAR(32) NOT NULL,
			name TEXT NOT NULL,
			expiry INTEGER NOT NULL,
			owner_id INT NOT NULL,
			PRIMARY KEY (uuid),
			FOREIGN KEY(owner_id) REFERENCES user (id)
		);
	`)

	if err != nil {
		return err
	}

	return nil
}
