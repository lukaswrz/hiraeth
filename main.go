package main

import (
	"database/sql"
	"log"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gin-gonic/gin"

	"github.com/urfave/cli/v2"

	"github.com/lukaswrz/hiraeth/config"
	"github.com/lukaswrz/hiraeth/schema"
)

func main() {
	var cf string

	app := &cli.App{
		Name:  "hiraeth",
		Usage: "share temporary files",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Usage:       "configuration file",
				Destination: &cf,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Argument error: %s", err.Error())
	}

	if cf == "" {
		cf = config.Locate()

		if cf == "" {
			log.Fatal("Unable to locate configuration file")
		}
	} else {
		_, err := os.Stat(cf)
		if err != nil {
			log.Fatalf("Specified configuration file not accessible: %s", err.Error())
		}
	}

	content, err := os.ReadFile(cf)
	if err != nil {
		log.Fatalf("Unable to read file: %s", err.Error())
	}

	c, err := config.Parse(content)
	if err != nil {
		log.Fatalf("Error while parsing configuration: %s", err.Error())
	}

	run(c)
}

func run(c config.Config) {
	err := os.MkdirAll(c.Data, os.ModePerm)
	if err != nil {
		log.Fatalf("Error while creating data directory: %s", err.Error())
	}

	db, err := sql.Open("sqlite3", c.DatabaseFile)
	if err != nil {
		log.Fatalf("Error while opening database: %s", err.Error())
	}
	schema.Init(db)
	if err != nil {
		log.Fatalf("Could not initialize the database: %s", err.Error())
	}

	// Schedule the deletion of temporary files.
	var files []file
	func() {
		rows, err := db.Query(`
			SELECT uuid, expiry
			FROM file
		`)
		if err != nil {
			log.Fatalf("Could not query database: %s", err.Error())
			return
		}
		defer rows.Close()

		for rows.Next() {
			var file file
			var expiry int64
			if err := rows.Scan(&file.UUID, &expiry); err != nil {
				log.Fatalf("Could not copy values from database: %s", err.Error())
				return
			}

			file.Expiry = time.Unix(expiry, 0)

			files = append(files, file)
		}
		if err = rows.Err(); err != nil {
			log.Fatalf("Error encountered during iteration: %s", err.Error())
			return
		}
	}()

	for _, file := range files {
		watch(file, c, db)
	}

	router := gin.Default()
	router.SetTrustedProxies(c.TrustedProxies)

	register(router, c, db)

	if err := router.Run(c.Address); err != nil {
		log.Fatal(err)
	}
}
