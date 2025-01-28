package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	ydbotel "github.com/ydb-platform/ydb-go-sdk-otel"
	"github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/balancers"
	"github.com/ydb-platform/ydb-go-sdk/v3/trace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/ernado/stor/internal/front"
)

func getYDBDSN() string {
	ips, err := net.LookupIP("ydb")
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		return "grpc://" + ip.String() + ":2136/local"
	}
	return ""
}

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, m *app.Telemetry) error {
		db, err := ydb.Open(ctx, getYDBDSN(),
			ydb.WithBalancer(balancers.SingleConn()), // Hack for local development.
			ydbotel.WithTraces(
				ydbotel.WithTracer(m.TracerProvider().Tracer("github.com/ydb-platform/ydb-go-sdk/v3")),
				ydbotel.WithDetails(trace.DetailsAll),
			),
		)
		if err != nil {
			return errors.Wrap(err, "open ydb")
		}
		defer func() {
			closeCtx := context.Background()
			_ = db.Close(closeCtx)
		}()

		// Initialize metadata storage.
		storage := front.NewYDBStorage(db, m.TracerProvider().Tracer("stor.front"))
		zctx.From(ctx).Info("Creating tables")
		tableCreateBackoff := backoff.NewExponentialBackOff()
		tableCreateBackoff.MaxInterval = time.Second * 2
		tableCreateBackoff.InitialInterval = time.Millisecond * 10
		tableCreateBackoff.MaxElapsedTime = time.Second * 10

		if err := backoff.RetryNotify(func() error {
			ctx, cancel := context.WithTimeout(ctx, time.Second*1)
			defer cancel()
			if err := storage.CreateTables(ctx); err != nil {
				return errors.Wrap(err, "create tables")
			}
			return nil
		}, backoff.WithContext(tableCreateBackoff, ctx), func(err error, duration time.Duration) {
			zctx.From(ctx).Warn("Retrying table creation", zap.Error(err), zap.Duration("duration", duration))
		}); err != nil {
			return errors.Wrap(err, "table creation")
		}

		// Instrument http client.
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

		// Initialize and instrument http server.
		handler := front.NewHandler(ctx, httpClient, storage, m.TracerProvider())
		srv := &http.Server{
			Addr:        ":8080",
			BaseContext: func(listener net.Listener) context.Context { return ctx },
			Handler: otelhttp.NewHandler(handler, "",
				otelhttp.WithTracerProvider(m.TracerProvider()),
				otelhttp.WithMeterProvider(m.MeterProvider()),
				otelhttp.WithPropagators(m.TextMapPropagator()),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					switch r.URL.Path {
					case "/register":
						return "http.Register"
					case "/upload":
						return "http.Upload"
					case "/health":
						return "http.Health"
					default:
						if strings.HasPrefix(r.URL.Path, "/download/") {
							return "http.Download"
						}
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
