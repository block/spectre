package config

import "github.com/alecthomas/kong"

type Config struct {
	Level string `default:"info" help:"Minimum level."`
}

type Plain struct {
	Name string `json:"name"`
}

func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(err)
	}
	return config
}

func Literal() Config { // want `Literal returns Config without calling kong.ApplyDefaults`
	return Config{Level: "info"}
}

func Pointer() (*Config, error) { // want `Pointer returns Config without calling kong.ApplyDefaults`
	return &Config{}, nil
}

func Kept() Config { //nolint:configdefaults kept for the test
	return Config{}
}

func NewPlain() Plain { return Plain{} }

func (c Config) WithLevel(level string) Config {
	c.Level = level
	return c
}
