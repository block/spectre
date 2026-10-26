package flags

type Config struct {
	Explicit string `env:"SPECTRE_FOO" help:"Explicit variable."` // want `remove the env:"SPECTRE_FOO" tag from Explicit; kong.DefaultEnvars names its variable`
	Empty    string `env:"" help:"Empty variable."`               // want `remove the env:"" tag from Empty`
	Version  bool   `env:"-" help:"Print the version and exit."`
	Plain    string `help:"No variable."`
	Command  struct {
		Token string `env:"SPECTRE_TOKEN" help:"Token."` // want `remove the env:"SPECTRE_TOKEN" tag from Token`
	} `cmd:"" help:"Run a command."`
	Kept string `env:"SPECTRE_KEPT" help:"Kept."` //nolint:kongenv // kept for the test
}

type NoEnv struct {
	Level string `default:"info" help:"Minimum level."`
}

// Other libraries, such as caarlos0/env, also read env tags.
type Other struct {
	Home string `env:"HOME"`
	Path string `env:"PATH" json:"path"`
}
