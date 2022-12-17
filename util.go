package main

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

type file struct {
	UUID   string
	Name   string
	Expiry time.Time
}

type user struct {
	ID       int
	Name     string
	Password string
}

func watch(file file, data string, db *sql.DB, logger *log.Logger) {
	logger.Printf("Watching %s", file.UUID)

	rm := func(uuid string, data string, db *sql.DB) {
		logger.Printf("Deleting %s", file.UUID)

		if err := os.Remove(filepath.Join(data, uuid)); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Fatalf("Unable to remove file with UUID %s: %s", uuid, err.Error())
		}

		_, err := db.Exec(`
			DELETE FROM file
			WHERE uuid = ?
		`, uuid)
		if err != nil {
			logger.Fatalf("Unable to delete file entry from database: %s", err.Error())
		}
	}

	diff := file.Expiry.Unix() - time.Now().Unix()
	if diff <= 0 {
		rm(file.UUID, data, db)
	} else {
		time.AfterFunc(time.Duration(diff)*time.Second, func() {
			rm(file.UUID, data, db)
		})
	}
}
