package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Address      string `toml:"address"`
	Name         string `toml:"name"`
	Data         string `toml:"data"`
	DatabaseFile string `toml:"database_file"`
	Redis        Redis  `toml:"redis"`
}

type Redis struct {
	Connections int      `toml:"connections"`
	Network     string   `toml:"network"`
	Address     string   `toml:"address"`
	Password    string   `toml:"password"`
	KeyPairs    []string `toml:"key_pairs"`
}

func Parse(data []byte) (Config, error) {
	c := Config{
		Address:      "localhost:8080",
		Name:         "hiraeth",
		Data:         "data",
		DatabaseFile: "hiraeth.db",
		Redis: Redis{
			Network:     "tcp",
			Address:     "localhost:6379",
			Connections: 10,
			Password:    "",
		},
	}

	err := toml.Unmarshal(data, &c)
	if err != nil {
		return Config{}, err
	}

	if c.Data == "" || c.Name == "" {
		return Config{}, errors.New("Default values are not defined")
	}

	return c, nil
}

func Locate() string {
	name := "hiraeth.toml"

	paths := []string{
		filepath.Join("/etc/hiraeth", name),
		name,
	}

	found := ""
	for _, path := range paths {
		_, err := os.Stat(path)
		if err != nil {
			continue
		}

		found = path
	}

	return found
}
