package state

import "example.com/architecture/internal/cli/terminal" // want `only internal/cli may import example.com/architecture/internal/cli/terminal`

var _ = terminal.Bold
