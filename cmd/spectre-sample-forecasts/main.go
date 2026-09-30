package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/sample"
)

func main() {
	var cli struct {
		Log     logger.Config    `embed:""`
		Version kong.VersionFlag `help:"Print the version and exit."`
		Listen  string           `default:"127.0.0.1:50053" help:"Address for the plaintext HTTP server: host:port, or unix:<path|@abstract>."`
		Weather string           `default:"internal/sample/testdata/weather.json" type:"existingfile" help:"Sample weather, keyed by location. Only the forecasts are served."`
	}
	kctx := kong.Parse(&cli, kong.Vars{"version": internal.Version})
	log := logger.New(cli.Log, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	data, err := os.ReadFile(cli.Weather)
	kctx.FatalIfErrorf(errors.Wrap(err, "read sample weather"))
	provider, err := sample.NewForecastProvider(data)
	kctx.FatalIfErrorf(err)
	listener, err := netaddr.ParseListen(cli.Listen).Listen(ctx)
	kctx.FatalIfErrorf(errors.Wrap(err, "listen for forecast requests"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	server := sample.NewServer(mux, log)
	log.InfoContext(ctx, "Sample forecast provider listening", "address", listener.Addr().String())
	kctx.FatalIfErrorf(errors.Wrap(server.Serve(ctx, listener), "serve sample forecasts"))
}
