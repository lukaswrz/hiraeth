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
	"github.com/gin-contrib/sessions/redis"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gin-gonic/gin"

	"github.com/urfave/cli/v2"
)

type config struct {
	Address        string      `toml:"address"`
	Name           string      `toml:"name"`
	Data           string      `toml:"data"`
	DatabaseFile   string      `toml:"database_file"`
	TrustedProxies []string    `toml:"trusted_proxies"`
	InlineTypes    []string    `toml:"inline_types"`
	Redis          redisConfig `toml:"redis"`
}

type redisConfig struct {
	Connections int      `toml:"connections"`
	Network     string   `toml:"network"`
	Address     string   `toml:"address"`
	Password    string   `toml:"password"`
	KeyPairs    []string `toml:"key_pairs"`
}

func main() {
	logger := log.New(os.Stderr, "hiraeth: ", log.Lshortfile)

	c := config{
		Address:      "localhost:8080",
		DatabaseFile: "hiraeth.db",
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
					readConfig(cf, paths, toml.Unmarshal, &c, logger)
					db := getDB(c, logger)
					initData(c, logger)

					// Schedule the deletion of temporary files.
					var files []file
					func() {
						rows, err := db.Query(`
							SELECT uuid, expiry
							FROM file
						`)
						if err != nil {
							logger.Fatalf("Could not query database: %s", err.Error())
							return
						}
						defer rows.Close()

						for rows.Next() {
							var file file
							var expiry int64
							if err := rows.Scan(&file.UUID, &expiry); err != nil {
								logger.Fatalf("Could not copy values from database: %s", err.Error())
								return
							}

							file.Expiry = time.Unix(expiry, 0)

							files = append(files, file)
						}
						if err = rows.Err(); err != nil {
							logger.Fatalf("Error encountered during iteration: %s", err.Error())
							return
						}
					}()

					for _, file := range files {
						watch(file, c.Data, db, logger)
					}

					router := gin.Default()
					router.SetTrustedProxies(c.TrustedProxies)

					kp := [][]byte{}
					for _, value := range c.Redis.KeyPairs {
						kp = append(kp, []byte(value))
					}
					store, err := redis.NewStore(c.Redis.Connections, c.Redis.Network, c.Redis.Address, c.Redis.Password, kp...)
					if err != nil {
						logger.Fatalf("Unable to create Redis store: %s", err.Error())
					}

					register(router, db, []gin.HandlerFunc{
						sessions.Sessions("session", store),
					}, logger, c.Data, c.InlineTypes)

					if err := router.Run(c.Address); err != nil {
						logger.Fatal(err)
					}

					return nil
				},
			},
			{
				Name:    "user",
				Aliases: []string{"u"},
				Usage:   "user",
				Subcommands: []*cli.Command{
					{
						Name:  "create",
						Usage: "create a new user",
						Action: func(ctx *cli.Context) error {
							readConfig(cf, paths, toml.Unmarshal, &c, logger)
							db := getDB(c, logger)
							initData(c, logger)

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
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		logger.Fatalf("Argument error: %s", err.Error())
	}
}

func readConfig(path string, paths []string, unmarshal func(data []byte, v interface{}) error, v interface{}, logger *log.Logger) {
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
			logger.Fatal("Unable to locate configuration file")
		}
	} else {
		_, err = os.Stat(path)
		if err != nil {
			logger.Fatalf("Could not stat %s: %s", path, err.Error())
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		logger.Fatalf("Unable to read configuration file %s: %s", path, err.Error())
	}

	err = unmarshal(content, v)
	if err != nil {
		logger.Fatalf("Unable to unmarshal configuration file %s: %s", path, err.Error())
	}
}

func getDB(c config, logger *log.Logger) *sql.DB {
	db, err := sql.Open("sqlite3", c.DatabaseFile)
	if err != nil {
		logger.Fatalf("Error while opening database: %s", err.Error())
	}
	err = initdb(db)
	if err != nil {
		logger.Fatalf("Could not initialize the database: %s", err.Error())
	}

	return db
}

func initData(c config, logger *log.Logger) {
	err := os.MkdirAll(c.Data, os.ModePerm)
	if err != nil {
		logger.Fatalf("Error while creating data directory: %s", err.Error())
	}
}

func initdb(db *sql.DB) error {
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
			password TEXT,
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
