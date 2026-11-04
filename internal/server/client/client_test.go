package client_test

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/storage"
)

func TestClientReachesServerThroughSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) //nolint:errcheck // Best-effort cleanup of a temp dir.
	socket := filepath.Join(dir, "egress.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	assert.NoError(t, err)
	hosts := make(chan string, 1)
	service := server.NewService(storage.NewMemory(), time.Minute).Handler()
	httpServer := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			hosts <- request.Host
			service.ServeHTTP(w, request)
		}),
	}
	go httpServer.Serve(listener)                //nolint:errcheck // Serve always returns an error once the server closes.
	t.Cleanup(func() { _ = httpServer.Close() }) //nolint:errcheck // Best-effort shutdown of the test server.

	// The host does not resolve, so only the socket can reach the server.
	spectre := client.New(client.Config{URL: "http://spectre.mesh.invalid", Socket: socket})
	states, err := spectre.ListStates(t.Context(), model.StateFilter{})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(states))
	assert.Equal(t, "spectre.mesh.invalid", <-hosts)
}
