package tags

type Config struct {
	Explicit string `env:"SPECTRE_FOO" help:"Explicit variable."` // want `remove the env:"SPECTRE_FOO" tag from Explicit; kong.DefaultEnvars names its variable`
	Empty    string `env:"" help:"Empty variable."`               // want `remove the env:"" tag from Empty`
	Version  bool   `env:"-" help:"Print the version and exit."`
	Plain    string `help:"No variable."`
	Command  struct {
		Token string `env:"SPECTRE_TOKEN" help:"Token."` // want `remove the env:"SPECTRE_TOKEN" tag from Token`
	} `cmd:"" help:"Run a command."`
	Kept string `env:"SPECTRE_KEPT" help:"Kept."` //nolint:forbiddentag // kept for the test
}

type Message struct {
	ID     string `json:"id"`
	Home   string `env:"HOME"`                // want `remove the env:"HOME" tag from Home`
	Secret string `env:"SECRET" yaml:"secret"` // want `remove the env:"SECRET" tag from Secret` `remove the yaml:"secret" tag from Secret; encode with JSON`
	Hidden string `yaml:"-"`
}
