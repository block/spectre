package model

// Listing is what the SPECTRE server lists: the states of the reporting
// mirrors, the recorded divergences, and each service environment's call total.
type Listing struct {
	States      []State
	Divergences []Divergence
	CallTotals  []CallTotal
}
