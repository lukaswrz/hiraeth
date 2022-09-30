package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"syscall"

	_ "github.com/mattn/go-sqlite3"

	"github.com/urfave/cli/v2"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/lukaswrz/hiraeth/config"
	"github.com/lukaswrz/hiraeth/schema"
)

func getDB(c config.Config) *sql.DB {
	db, err := sql.Open("sqlite3", c.DatabaseFile)
	if err != nil {
		log.Fatalf("Error while opening database: %s", err.Error())
	}
	schema.Init(db)
	if err != nil {
		log.Fatalf("Could not initialize the database: %s", err.Error())
	}

	return db
}

func getConfig(cf string) config.Config {
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

	return c
}

func main() {
	var cf string

	app := &cli.App{
		Name:  "hiraethctl",
		Usage: "administration tools for hiraeth",
		Action: func(ctx *cli.Context) error {
			return nil
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Usage:       "configuration file",
				Destination: &cf,
			},
		},
		Commands: []*cli.Command{
			{
				Name:    "user",
				Aliases: []string{"u"},
				Usage:   "user",
				Action: func(*cli.Context) error {
					return nil
				},
				Subcommands: []*cli.Command{
					{
						Name:  "create",
						Usage: "create a new user",
						Action: func(ctx *cli.Context) error {
							c := getConfig(cf)
							db := getDB(c)

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

	app.Setup()

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error: %s", err.Error())
	}
}
