package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	rpc "buf.build/gen/go/open-feature/flagd/bufbuild/connect-go/sync/v1/syncv1connect"
	"github.com/open-feature/flagd/core/pkg/logger"
	iservice "github.com/open-feature/flagd/core/pkg/service"
	syncStore "github.com/open-feature/flagd/core/pkg/sync-store"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	server        *http.Server
	metricsServer *http.Server
	Logger        *logger.Logger
	handler       handler
	config        iservice.Configuration
}

func NewServer(ctx context.Context, logger *logger.Logger) *Server {
	syncStore := syncStore.NewSyncStore(ctx, logger)
	return &Server{
		handler: handler{
			logger:    logger,
			syncStore: syncStore,
		},
		Logger: logger,
	}
}

func (s *Server) Serve(ctx context.Context, svcConf iservice.Configuration) error {
	s.config = svcConf

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(s.startServer)
	g.Go(s.startMetricsServer)
	g.Go(func() error {
		<-gCtx.Done()
		if s.server != nil {
			if err := s.server.Shutdown(gCtx); err != nil {
				return err
			}
		}
		return nil
	})
	g.Go(func() error {
		<-gCtx.Done()
		if s.metricsServer != nil {
			if err := s.metricsServer.Shutdown(gCtx); err != nil {
				return err
			}
		}
		return nil
	})

	err := g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) startServer() error {
	var lis net.Listener
	var err error
	mux := http.NewServeMux()
	address := fmt.Sprintf(":%d", s.config.Port)
	lis, err = net.Listen("tcp", address)
	if err != nil {
		return err
	}
	path, handler := rpc.NewFlagSyncServiceHandler(&s.handler)
	mux.Handle(path, handler)

	s.server = &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
	}

	if err := s.server.Serve(
		lis,
	); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (s *Server) startMetricsServer() error {
	s.Logger.Info(fmt.Sprintf("binding metrics to %d", s.config.MetricsPort))
	s.metricsServer = &http.Server{
		ReadHeaderTimeout: 3 * time.Second,
		Addr:              fmt.Sprintf(":%d", s.config.MetricsPort),
	}
	s.metricsServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/readyz":
			if s.config.ReadinessProbe() {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusPreconditionFailed)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := s.metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}