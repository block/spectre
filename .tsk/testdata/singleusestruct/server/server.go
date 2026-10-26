package server

// Config is the package config, which a command in another package embeds.
type Config struct {
	Bind string `help:"Address to listen on."`
}

type QueryConfig struct { // want `QueryConfig is used only by DivergencesConfig; move its fields into DivergencesConfig`
	JSON bool `help:"Print JSON instead of text."`
}

type DivergencesConfig struct {
	Query   QueryConfig `embed:""`
	Service string      `help:"Only list divergences of this service."`
}

type listConfig struct { // want `listConfig is used only by listCommand; move its fields into listCommand`
	Limit int `help:"Most entries to list."`
}

type listCommand struct {
	*listConfig
	Verbose bool `help:"Print more."`
}

type SharedConfig struct {
	Timeout int `help:"Seconds to wait."`
}

type FirstCommand struct {
	Shared SharedConfig `embed:""`
}

type SecondCommand struct {
	Shared SharedConfig `embed:""`
}

type ParamConfig struct {
	Name string `help:"Name to use."`
}

type ParamCommand struct {
	Param ParamConfig `embed:""`
}

func Use(config ParamConfig) string { return config.Name }

type MethodConfig struct {
	Path string `help:"Path to read."`
}

func (c MethodConfig) Validate() error { return nil }

type MethodCommand struct {
	Method MethodConfig `embed:""`
}

type LiteralConfig struct {
	Count int `help:"How many."`
}

type LiteralCommand struct {
	Literal LiteralConfig `embed:""`
}

func Default() LiteralCommand {
	return LiteralCommand{Literal: LiteralConfig{Count: 1}}
}

type TwiceConfig struct {
	Port int `help:"Port to use."`
}

type TwiceCommand struct {
	Primary   TwiceConfig `embed:"" prefix:"primary-"`
	Secondary TwiceConfig `embed:"" prefix:"secondary-"`
}

type PrefixConfig struct {
	Port int `help:"Port to use."`
}

type PrefixCommand struct {
	Prefix PrefixConfig `embed:"" prefix:"prefix-"`
}

type subCommand struct {
	Force bool `help:"Overwrite."`
}

type parentCommand struct {
	Sub subCommand `cmd:"" help:"Run the subcommand."`
}

type TestedConfig struct { // want `TestedConfig is used only by TestedCommand; move its fields into TestedCommand`
	Debug bool `help:"Print debug output."`
}

type TestedCommand struct {
	Tested TestedConfig `embed:""`
}

type plain struct { // want `plain is used only by plainHolder; move its fields into plainHolder`
	Name string
}

type plainHolder struct {
	Plain plain
}

type hand struct {
	Text string
}

type generatedHolder struct {
	Message GeneratedMessage
}
