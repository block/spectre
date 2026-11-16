// Package integration tests a whole mirror: proctor runs Procfile.integration
// for each test, with the sample services, the proxies and the SPECTRE server
// as processes of their own, and the tests drive it over its unix sockets.
//
// The tests are skipped with -short, so the unit test run leaves them out.
package integration
