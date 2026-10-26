package flags

type testConfig struct {
	Name string `env:"TEST_NAME" help:"Read without DefaultEnvars."`
}

var _ = testConfig{}
