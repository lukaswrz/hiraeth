package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gin-gonic/gin"

	"github.com/urfave/cli/v2"
)

type config struct {
	Address        string   `toml:"address"`
	Name           string   `toml:"name"`
	Data           string   `toml:"data"`
	DatabaseFile   string   `toml:"database_file"`
	SessionSecret  string   `toml:"session_secret"`
	ChunkSize      int64    `toml:"chunk_size"`
	Timeout        int      `toml:"timeout"`
	TrustedProxies []string `toml:"trusted_proxies"`
	InlineTypes    []string `toml:"inline_types"`
}

func main() {
	log.SetFlags(log.Lshortfile | log.Ldate | log.Ltime)
	log.SetPrefix("hiraeth: ")

	c := config{
		Address:      "localhost:8080",
		DatabaseFile: "hiraeth.db",
		ChunkSize:    1024 * 1024 * 32,
		Timeout:      60,
	}

	paths := []string{
		"hiraeth.toml",
		"/etc/hiraeth/hiraeth.toml",
	}

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
		Commands: []*cli.Command{
			{
				Name:  "run",
				Usage: "run hiraeth",
				Action: func(ctx *cli.Context) error {
					readConfig(cf, paths, toml.Unmarshal, &c)
					db := getDB(c)
					initData(c)

					// Schedule the deletion of temporary files and delete unfinished ones.
					func() {
						rows, err := db.Query(`
							SELECT uuid, expiry, done
							FROM file
						`)
						if err != nil {
							log.Fatalf("Could not query database: %s", err.Error())
							return
						}
						defer func() {
							err := rows.Close()
							if err != nil {
								log.Fatalf("Unable to close rows: %s", err.Error())
								return
							}
						}()

						for rows.Next() {
							var fileuuid string
							var expiry int64
							var done bool
							if err := rows.Scan(&fileuuid, &expiry, &done); err != nil {
								log.Fatalf("Could not copy values from database: %s", err.Error())
								return
							}

							if done {
								watch(fileuuid, time.Unix(expiry, 0), c.Data, db)
							} else {
								log.Printf("File %s is unfinished", fileuuid)
								remove(fileuuid, c.Data, db)
							}
						}
						if err = rows.Err(); err != nil {
							log.Fatalf("Error encountered during iteration: %s", err.Error())
							return
						}
					}()

					router := gin.Default()
					router.SetTrustedProxies(c.TrustedProxies)

					store := cookie.NewStore([]byte(c.SessionSecret))

					router.Use(sessions.Sessions("session", store))

					register(router, db, c.Data, c.InlineTypes, c.ChunkSize, c.Timeout)

					if err := router.Run(c.Address); err != nil {
						log.Fatal(err)
					}

					return nil
				},
			},
			{
				Name:  "register",
				Usage: "create a new user",
				Action: func(ctx *cli.Context) error {
					readConfig(cf, paths, toml.Unmarshal, &c)
					db := getDB(c)
					initData(c)

					for _, name := range ctx.Args().Slice() {
						fmt.Fprintf(os.Stderr, "Enter password for new user %s: ", name)
						bytePassword, err := term.ReadPassword(int(syscall.Stdin))
						fmt.Fprint(os.Stderr, "\n")
						if err != nil {
							return err
						}
						hashedPassword, err := bcrypt.GenerateFromPassword(bytePassword, 12)
						if err != nil {
							return err
						}

						_, err = db.Exec(`
							INSERT INTO user (name, password)
							VALUES (?, ?)
						`, name, hashedPassword)
						if err != nil {
							return err
						}
					}

					return nil
				},
			},
			{
				Name:  "revoke",
				Usage: "delete an existing user",
				Action: func(ctx *cli.Context) error {
					readConfig(cf, paths, toml.Unmarshal, &c)
					db := getDB(c)
					initData(c)

					for _, name := range ctx.Args().Slice() {
						_, err := db.Exec(`
							DELETE FROM user
							WHERE id = ?
						`, name)
						if err != nil {
							return err
						}
					}

					return nil
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Argument error: %s", err.Error())
	}
}

func readConfig(path string, paths []string, unmarshal func(data []byte, v interface{}) error, v interface{}) {
	var err error

	if path == "" {
		for _, p := range paths {
			_, err = os.Stat(p)
			if err != nil {
				continue
			}

			path = p
		}

		if path == "" {
			log.Fatal("Unable to locate configuration file")
		}
	} else {
		_, err = os.Stat(path)
		if err != nil {
			log.Fatalf("Could not stat %s: %s", path, err.Error())
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Unable to read configuration file %s: %s", path, err.Error())
	}

	err = unmarshal(content, v)
	if err != nil {
		log.Fatalf("Unable to unmarshal configuration file %s: %s", path, err.Error())
	}
}

func getDB(c config) *sql.DB {
	db, err := sql.Open("sqlite3", c.DatabaseFile)
	if err != nil {
		log.Fatalf("Error while opening database: %s", err.Error())
	}
	err = initDB(db)
	if err != nil {
		log.Fatalf("Could not initialize the database: %s", err.Error())
	}

	return db
}

func initData(c config) {
	err := os.MkdirAll(c.Data, os.ModePerm)
	if err != nil {
		log.Fatalf("Error while creating data directory: %s", err.Error())
	}
}

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			password TEXT NOT NULL,
			UNIQUE(name)
		);

		CREATE TABLE IF NOT EXISTS file(
			uuid CHAR(32) PRIMARY KEY,
			name TEXT NOT NULL,
			expiry INTEGER NOT NULL,
			password TEXT,
			done INTEGER NOT NULL,
			owner_id INTEGER NOT NULL REFERENCES user(id)
		);
	`)

	if err != nil {
		return err
	}

	return nil
}
