package model

import (
	"maps"
	"time"
)

// State is what the mirror of a service environment runs and holds, as its
// latest update reported it.
type State struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// Revision is the commit of the candidate the mirror runs.
	Revision       string `json:"revision"`
	SpectreVersion string `json:"spectre_version"`
	// Uptime is how long the mirror has run. Without quarantines, the candidate
	// has been healthy that long, because quarantine lasts for the process.
	Uptime time.Duration `json:"uptime"`
	// Quarantines holds the quarantine of each direction whose proxy has one.
	Quarantines map[Direction]Quarantine `json:"quarantines,omitempty"`
}

func (State) update() {}

// Quarantined returns the state with the quarantine of the proxy in direction.
func (s State) Quarantined(direction Direction, quarantine Quarantine) State {
	quarantines := make(map[Direction]Quarantine, len(s.Quarantines)+1)
	maps.Copy(quarantines, s.Quarantines)
	quarantines[direction] = quarantine
	s.Quarantines = quarantines
	return s
}
