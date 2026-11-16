// Package integration runs a whole mirror for tests. The spectre proxy and
// server run as processes of their own, in front of reference, candidate and
// dependency services that the test defines in Go. Every listener takes a free
// port from the operating system, so tests run in parallel without conflicts.
//
// Start skips the test with -short, so the unit test run leaves it out.
package integration
