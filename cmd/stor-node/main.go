package main

import (
	"context"
	"net/http"
	"os"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/ernado/stor/internal/node"
)

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, m *app.Telemetry) error {
		chunks, err := node.NewChunks(os.Getenv("CHUNKS_DIR"), m.TracerProvider(), m.MeterProvider())
		if err != nil {
			return errors.Wrap(err, "init chunks")
		}
		srv := &http.Server{
			Addr: ":8080",
			Handler: otelhttp.NewHandler(node.NewHandler(chunks), "",
				otelhttp.WithTracerProvider(m.TracerProvider()),
				otelhttp.WithMeterProvider(m.MeterProvider()),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					switch r.Method {
					case http.MethodGet:
						return "ChunksGet"
					case http.MethodPut:
						return "ChunksPut"
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
	})
}
