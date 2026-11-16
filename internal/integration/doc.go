// Package integration runs a whole mirror for tests. The spectre proxy and
// server run as processes of their own, in front of reference, candidate and
// dependency services that the test defines in Go. Every listener is opened on
// a free port and handed to its process, so tests run in parallel without
// conflicts. Tests check the state the proxy reports to the server.
//
// Start skips the test with -short, so the unit test run leaves it out.
package integration
