package model

// Update is one message of the stream a mirror sends the SPECTRE server. Every
// stream begins with the mirror's whole State, and Calls replaces its counts.
//
//sumtype:decl
type Update interface{ update() }

// QuarantineUpdate adds the quarantine of the proxy in Direction to the state.
type QuarantineUpdate struct {
	Direction  Direction
	Quarantine Quarantine
}

func (QuarantineUpdate) update() {}
