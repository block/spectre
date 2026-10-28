package state_test

import "example.com/architecture/internal/cli/terminal" // want `only internal/cli may import`

var _ = terminal.Bold
