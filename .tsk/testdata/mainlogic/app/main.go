package main

type cli struct{}

func (c *cli) Run() error { return helper() }

func (c *cli) AfterApply() error { return nil } // want `AfterApply must move to internal/`

func helper() error { return nil } // want `helper must move to internal/`

func startup() error { return nil } //nolint:mainlogic startup only

func main() { _ = (&cli{}).Run() }
