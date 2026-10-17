package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/service"
	"github.com/velovsky/insurance-product-service/internal/store"
)

func main() {
	seed := flag.Bool("seed", os.Getenv("SEED") == "true", "create a demo product with a published tariff")
	flag.Parse()

	addr := os.Getenv("GRPC_ADDR")
	if addr == "" {
		addr = ":50051"
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(addr, *seed, log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr string, seed bool, log *slog.Logger) error {
	repo := store.NewMemory()
	clock := func() time.Time { return time.Now().UTC() }
	if seed {
		if err := seedDemo(repo, clock()); err != nil {
			return err
		}
		log.Info("seeded demo product", "code", demoProductCode)
	}

	srv := grpc.NewServer(grpc.UnaryInterceptor(logRequests(log)))
	productv1.RegisterProductServiceServer(srv, service.NewProductServer(repo, clock))
	productv1.RegisterQuoteServiceServer(srv, service.NewQuoteServer(repo, clock))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	reflection.Register(srv)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		srv.GracefulStop()
	}()

	log.Info("listening", "addr", lis.Addr().String())
	return srv.Serve(lis)
}

func logRequests(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		md, _ := metadata.FromIncomingContext(ctx)
		log.Info("rpc",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"actor", first(md.Get(service.ActorHeader)),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return resp, err
	}
}

func first(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
