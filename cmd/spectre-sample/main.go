package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/grpcreflect"
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/sample"
	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
)

func main() {
	var cli struct {
		Log     logger.Config    `embed:""`
		Version kong.VersionFlag `help:"Print the version and exit."`
		Listen  string           `default:"127.0.0.1:50051" help:"Address for the plaintext Connect server."`
		Data    string           `default:"internal/sample/testdata/users.json" type:"existingfile" help:"ProtoJSON sample users."`
	}
	cli.Log = logger.NewConfig()
	kctx := kong.Parse(&cli, kong.Vars{"version": internal.Version})
	log := logger.New(cli.Log, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	data, err := os.ReadFile(cli.Data)
	kctx.FatalIfErrorf(errors.Wrap(err, "read sample users"))
	service, err := sample.New(data)
	kctx.FatalIfErrorf(err)
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cli.Listen)
	kctx.FatalIfErrorf(errors.Wrap(err, "listen for Connect requests"))
	mux := http.NewServeMux()
	path, handler := samplepbconnect.NewUserServiceHandler(service)
	mux.Handle(path, handler)
	reflector := grpcreflect.NewStaticReflector(samplepbconnect.UserServiceName)
	path, handler = grpcreflect.NewHandlerV1(reflector)
	mux.Handle(path, handler)
	path, handler = grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(path, handler)
	sampleServer := sample.NewServer(mux, log)
	log.InfoContext(ctx, "Sample Connect server listening", "address", listener.Addr().String())
	kctx.FatalIfErrorf(errors.Wrap(sampleServer.Serve(ctx, listener), "serve sample Connect"))
}
