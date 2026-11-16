// Package integration runs a whole mirror for tests. The SPECTRE server and
// proxy run in goroutines of the test, in front of reference, candidate and
// dependency services that the test defines in Go. Every listener is opened on
// a free port and handed over, so tests run in parallel without conflicts.
// Tests check the state the proxy reports to the server.
//
// Tests that use it carry the integration build tag, so only a run with
// -tags=integration includes them.
package integration
