package main

import (
	"context"
	"net"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/ernado/stor/internal/front"
)

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, m *app.Telemetry) error {
		ctx = zctx.WithOpenTelemetryZap(ctx)
		httpClient := &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport,
				otelhttp.WithTracerProvider(m.TracerProvider()),
				otelhttp.WithMeterProvider(m.MeterProvider()),
				otelhttp.WithPropagators(m.TextMapPropagator()),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					switch r.Method {
					case http.MethodGet:
						return "http.client.ChunksGet"
					case http.MethodPut:
						return "http.client.ChunksPut"
					default:
						return ""
					}
				}),
			),
		}
		srv := &http.Server{
			Addr:        ":8080",
			BaseContext: func(listener net.Listener) context.Context { return ctx },
			Handler: otelhttp.NewHandler(front.NewHandler(ctx, httpClient, m.TracerProvider()), "",
				otelhttp.WithTracerProvider(m.TracerProvider()),
				otelhttp.WithMeterProvider(m.MeterProvider()),
				otelhttp.WithPropagators(m.TextMapPropagator()),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					switch r.URL.Path {
					case "/register":
						return "http.Register"
					case "/upload":
						return "http.Upload"
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
		lg.Info("Server started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.Wrap(err, "listen and serve")
		}
		return nil
	},
		app.WithServiceName("stor.front"),
	)
}
