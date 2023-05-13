package main

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

func remove(uuid string, data string, db *sql.DB) {
	log.Printf("Deleting %s", uuid)

	if err := os.Remove(filepath.Join(data, uuid)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("Unable to remove file with UUID %s: %s", uuid, err.Error())
	}

	_, err := db.Exec(`
		DELETE FROM file
		WHERE uuid = ?
	`, uuid)
	if err != nil {
		log.Fatalf("Unable to delete file entry from database: %s", err.Error())
	}
}

func watch(uuid string, expiry time.Time, data string, db *sql.DB) {
	log.Printf("Watching %s", uuid)

	diff := expiry.Unix() - time.Now().Unix()
	if diff <= 0 {
		remove(uuid, data, db)
	} else {
		time.AfterFunc(time.Duration(diff)*time.Second, func() {
			remove(uuid, data, db)
		})
	}
}
