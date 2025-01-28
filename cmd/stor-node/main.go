package main

import (
	"context"
	"net"
	"net/http"
	"os"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/ernado/stor/internal/node"
)

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, m *app.Telemetry) error {
		ctx = zctx.WithOpenTelemetryZap(ctx)
		chunks, err := node.NewChunks(os.Getenv("CHUNKS_DIR"), m.TracerProvider(), m.MeterProvider())
		if err != nil {
			return errors.Wrap(err, "init chunks")
		}
		const listenPort = "8080"
		handler := node.NewHandler(chunks)
		// Initialize and instrument http server.
		srv := &http.Server{
			Addr:        ":" + listenPort,
			BaseContext: func(listener net.Listener) context.Context { return ctx },
			Handler: otelhttp.NewHandler(handler, "",
				otelhttp.WithTracerProvider(m.TracerProvider()),
				otelhttp.WithMeterProvider(m.MeterProvider()),
				otelhttp.WithPropagators(m.TextMapPropagator()),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					if r.URL.Path == "/health" {
						return "http.Health"
					}
					switch r.Method {
					case http.MethodGet:
						return "http.ChunksGet"
					case http.MethodPut:
						return "http.ChunksPut"
					default:
						return ""
					}
				}),
			),
		}
		go func() {
			// Graceful shutdown.
			<-ctx.Done()
			_ = srv.Shutdown(context.Background())
		}()
		go func() {
			// Use instrumented http client to register node in front.
			httpClient := &http.Client{
				Transport: otelhttp.NewTransport(http.DefaultTransport,
					otelhttp.WithTracerProvider(m.TracerProvider()),
					otelhttp.WithMeterProvider(m.MeterProvider()),
					otelhttp.WithPropagators(m.TextMapPropagator()),
					otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
						return "http.client.Register"
					}),
				),
			}
			if err := node.Register(ctx, httpClient, listenPort); err != nil {
				lg.Fatal("Register", zap.Error(err))
			}
		}()
		lg.Info("Server started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.Wrap(err, "listen and serve")
		}
		return nil
	},
		app.WithServiceName("stor.node"),
	)
}
