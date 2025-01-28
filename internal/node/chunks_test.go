package node

import (
	"bytes"
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	noopMeter "go.opentelemetry.io/otel/metric/noop"
	noopTracer "go.opentelemetry.io/otel/trace/noop"
)

func randomData(t testing.TB, n int) []byte {
	source := rand.NewSource(10)
	rnd := rand.New(source)
	data := make([]byte, n)
	if _, err := rnd.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestChunks(t *testing.T) {
	chunks, err := NewChunks(t.TempDir(), noopTracer.NewTracerProvider(), noopMeter.NewMeterProvider())
	require.NoError(t, err)

	data := randomData(t, 1024)
	ctx := context.Background()
	id := uuid.New()
	require.NoError(t, chunks.Write(ctx, id, bytes.NewReader(data)), "write")

	buf := new(bytes.Buffer)
	require.NoError(t, chunks.Read(ctx, id, buf), "read")
	require.Equal(t, data, buf.Bytes(), "read data should equal to written data")

	require.Error(t, chunks.Read(ctx, uuid.Nil, new(bytes.Buffer)), "read non-existent chunk should error")
}
