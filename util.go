package main

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/lukaswrz/hiraeth/config"
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

func watch(file file, c config.Config, db *sql.DB) {
	log.Printf("Watching %s", file.UUID)

	rm := func(uuid string, c config.Config, db *sql.DB) {
		log.Printf("Deleting %s", file.UUID)

		if err := os.Remove(filepath.Join(c.Data, uuid)); err != nil && !errors.Is(err, os.ErrNotExist) {
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

	diff := file.Expiry.Unix() - time.Now().Unix()
	if diff <= 0 {
		rm(file.UUID, c, db)
	} else {
		time.AfterFunc(time.Duration(diff)*time.Second, func() {
			rm(file.UUID, c, db)
		})
	}
}
