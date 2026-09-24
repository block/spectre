package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/sample"
	samplepb "github.com/block/spectre/internal/sample/pb"
)

func main() {
	var cli struct {
		Log     logger.Config    `embed:""`
		Version kong.VersionFlag `help:"Print the version and exit."`
		Listen  string           `default:"127.0.0.1:50051" help:"Address for the plaintext gRPC server."`
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
	kctx.FatalIfErrorf(errors.Wrap(err, "listen for gRPC requests"))
	server := grpc.NewServer()
	samplepb.RegisterUserServiceServer(server, service)
	reflection.Register(server)
	sampleServer := sample.NewServer(server, log)
	log.InfoContext(ctx, "Sample gRPC server listening", "address", listener.Addr().String())
	kctx.FatalIfErrorf(errors.Wrap(sampleServer.Serve(ctx, listener), "serve sample gRPC"))
}
