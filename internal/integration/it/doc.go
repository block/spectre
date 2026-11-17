//go:build integration

// Package it is a DSL for integration tests that run a whole mirror. Start runs
// the reference and candidate services, their dependencies, the SPECTRE server
// and the proxy in the test process, on free ports, so tests run in parallel.
// Settings give the services and the comparison scripts, inline as TypeScript
// or as a directory. Tests send requests through the mirror's listeners and
// check the responses, the requests each service received, and the calls and
// quarantines the proxy reports to the server.
//
// Its files carry the integration build tag, so only integration tests build it.
package it
