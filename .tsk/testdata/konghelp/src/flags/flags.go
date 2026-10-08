package flags

type Group struct {
	Level string `help:"Minimum level."`
}

type Base struct {
	Verbose bool `help:"Verbose output."`
}

type Config struct {
	Base
	Documented string `default:"x" help:"Documented flag."`
	Missing    string `default:"x"` // want `Missing needs a help:"..." tag`
	Empty      string `help:" "`     // want `Empty needs a help:"..." tag`
	Untagged   int    // want `Untagged needs a help:"..." tag`
	Arg        string `arg:""` // want `Arg needs a help:"..." tag`
	Command    struct {
		Output string `required:""` // want `Output needs a help:"..." tag`
	} `cmd:"" help:"Run a command."`
	Embedded Group  `embed:"" prefix:"group-"`
	Ignored  string `kong:"-"`
	Kept     string `default:"x"` //nolint:konghelp kept for the test
	private  string
}

type Plain struct {
	Name string `json:"name"`
}
