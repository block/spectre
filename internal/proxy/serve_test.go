package proxy_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/proxy"
)

func TestServeShutsDownEveryListenerWhenOneFails(t *testing.T) {
	first := listen(t)
	second := listen(t)
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	started := make(chan struct{})
	lifecycle := proxy.NewLifecycle(
		func(context.Context) {
			record("start")
			close(started)
		},
		func() { record("stop") },
		func(context.Context) error {
			record("drain")
			return nil
		},
	)
	bindings := []proxy.Binding{
		proxy.NewBinding(first, namedHandler("first")),
		proxy.NewBinding(second, namedHandler("second")),
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- proxy.Serve(t.Context(), proxy.NewConfig(), slog.New(slog.DiscardHandler), bindings, lifecycle)
	}()
	<-started
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	assert.Equal(t, "first", get(t, client, first))
	assert.Equal(t, "second", get(t, client, second))

	assert.NoError(t, first.Close())
	select {
	case err := <-serveDone:
		assert.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("serve did not stop after a listener failed")
	}
	mu.Lock()
	assert.Equal(t, []string{"start", "stop", "drain"}, events)
	mu.Unlock()
	response, err := client.Get("http://" + second.Addr().String())
	if err == nil {
		assert.NoError(t, response.Body.Close())
	}
	assert.Error(t, err)
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	return listener
}

func namedHandler(name string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, name)
	})
}

func get(t *testing.T, client *http.Client, listener net.Listener) string {
	t.Helper()
	response, err := client.Get("http://" + listener.Addr().String())
	assert.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	return string(body)
}
