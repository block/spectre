package flags

type Config struct {
	Level string `help:"Minimum level."`
}

type Plain struct {
	Name string `json:"name"`
}
