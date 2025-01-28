package node

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	noopMeter "go.opentelemetry.io/otel/metric/noop"
	noopTracer "go.opentelemetry.io/otel/trace/noop"
)

func TestNewHandler(t *testing.T) {
	chunks, err := NewChunks(t.TempDir(), noopTracer.NewTracerProvider(), noopMeter.NewMeterProvider())
	require.NoError(t, err)
	var (
		handler = NewHandler(chunks)
		server  = httptest.NewServer(handler)
		client  = NewClient(server.URL, server.Client(), noopTracer.NewTracerProvider())
		data    = randomData(t, 1024)
		ctx     = context.Background()
		id      = uuid.New()
	)
	require.NoError(t, client.Write(ctx, id, bytes.NewReader(data)), "write")
	buf := new(bytes.Buffer)
	require.NoError(t, client.Read(ctx, id, buf), "read")
	require.Equal(t, data, buf.Bytes(), "read data should equal to written data")
}
