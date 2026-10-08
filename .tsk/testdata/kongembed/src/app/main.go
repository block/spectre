package main

import "flags"

type cli struct {
	Embedded flags.Config  `embed:"" prefix:"x-"`
	Missing  flags.Config  `prefix:"y-"` // want `Missing must have an embed:"" tag to embed flags.Config`
	Pointer  *flags.Config // want `Pointer must have an embed:"" tag to embed flags.Config`
	Kept     flags.Config  //nolint:kongembed kept for the test
	Plain    flags.Plain
	Local    local
}

type local struct {
	Name string `help:"Name."`
}

func main() {
	var anonymous struct {
		Config flags.Config // want `Config must have an embed:"" tag to embed flags.Config`
	}
	_ = anonymous
	_ = cli{}
}
