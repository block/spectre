package model

import . "github.com/alecthomas/types/optional"

// EnvironmentStatus describes one service environment for display.
// Combined server results can hold multiple divergences in the same direction.
type EnvironmentStatus struct {
	Service, Environment string
	State                Option[State]
	Divergences          []Divergence
}
