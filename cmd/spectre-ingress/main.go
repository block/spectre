package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/ingress"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/schema"
)

func main() {
	var cli struct {
		Log         logger.Config    `embed:""`
		Version     kong.VersionFlag `help:"Print the version and exit."`
		CheckSchema struct {
			DescriptorSet string `arg:"" type:"existingfile" help:"Binary protobuf descriptor set, including imports."`
		} `cmd:"" help:"Validate a local protobuf descriptor set."`
		Serve struct {
			Listen            string        `default:"127.0.0.1:50050" help:"Address for the ingress HTTP server."`
			Reference         string        `required:"" help:"Reference backend URL."`
			Candidate         string        `required:"" help:"Candidate backend URL."`
			CandidateTimeout  time.Duration `default:"30s" help:"Maximum duration of a candidate request."`
			ReadHeaderTimeout time.Duration `default:"10s" help:"Maximum duration for reading request headers."`
			ShutdownTimeout   time.Duration `default:"10s" help:"Maximum graceful shutdown duration."`
		} `cmd:"" help:"Mirror HTTP requests to reference and candidate backends."`
	}
	cli.Log = logger.NewConfig()
	kctx := kong.Parse(&cli, kong.Vars{"version": internal.Version})
	log := logger.New(cli.Log, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)

	switch kctx.Command() {
	case "check-schema <descriptor-set>":
		data, err := os.ReadFile(cli.CheckSchema.DescriptorSet)
		kctx.FatalIfErrorf(errors.Wrap(err, "read descriptor set"))
		_, err = schema.New(data)
		kctx.FatalIfErrorf(err)
		log.InfoContext(ctx, "Descriptor set is valid", "path", cli.CheckSchema.DescriptorSet)
	case "serve":
		reference, err := parseBackendURL(cli.Serve.Reference)
		kctx.FatalIfErrorf(err)
		candidate, err := parseBackendURL(cli.Serve.Candidate)
		kctx.FatalIfErrorf(err)
		transport := ingress.NewTransport()
		defer transport.CloseIdleConnections()
		handler := ingress.New(reference, candidate, cli.Serve.CandidateTimeout, transport, log)
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cli.Serve.Listen)
		kctx.FatalIfErrorf(errors.Wrap(err, "listen for HTTP requests"))
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
		protocols.SetUnencryptedHTTP2(true)
		server := &http.Server{
			Handler:           handler,
			Protocols:         protocols,
			ReadHeaderTimeout: cli.Serve.ReadHeaderTimeout,
			BaseContext: func(net.Listener) context.Context {
				return ctx
			},
		}
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(listener) }()
		log.InfoContext(ctx, "Ingress proxy listening", "address", listener.Addr().String())

		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				kctx.FatalIfErrorf(errors.Wrap(err, "serve ingress HTTP"))
			}
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), cli.Serve.ShutdownTimeout)
			defer cancel()
			shutdownErrors := make([]error, 0, 3)
			if err := server.Shutdown(shutdownContext); err != nil {
				shutdownErrors = append(shutdownErrors, errors.Wrap(err, "shut down HTTP server"))
			}
			if err := handler.Shutdown(shutdownContext); err != nil {
				shutdownErrors = append(shutdownErrors, err)
			}
			if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
				shutdownErrors = append(shutdownErrors, errors.Wrap(err, "serve ingress HTTP"))
			}
			kctx.FatalIfErrorf(errors.Join(shutdownErrors...))
		}
	}
}

func parseBackendURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.Wrap(err, "parse backend URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.Errorf("backend URL must be an absolute HTTP or HTTPS URL: %q", value)
	}
	return parsed, nil
}
