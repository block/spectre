package main

import (
	srv "example.com/m/server"
)

type cli struct {
	Server srv.Config `embed:""`
}

func main() {}
