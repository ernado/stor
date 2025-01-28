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

type randomData struct {
	rnd *rand.Rand
}

func newRandomData() *randomData {
	return &randomData{rnd: rand.New(rand.NewSource(0))}
}

func (r *randomData) New(tb testing.TB, n int) []byte {
	data := make([]byte, n)
	if _, err := r.rnd.Read(data); err != nil {
		tb.Fatal(err)
	}
	return data
}

func TestChunks(t *testing.T) {
	chunks, err := NewChunks(t.TempDir(), noopTracer.NewTracerProvider(), noopMeter.NewMeterProvider())
	require.NoError(t, err)

	rd := newRandomData()
	data := rd.New(t, 1024)
	ctx := context.Background()
	id := uuid.New()
	require.NoError(t, chunks.Write(ctx, id, bytes.NewReader(data)), "write")

	buf := new(bytes.Buffer)
	require.NoError(t, chunks.Read(ctx, id, buf), "read")
	require.Equal(t, data, buf.Bytes(), "read data should equal to written data")

	require.Error(t, chunks.Read(ctx, uuid.Nil, new(bytes.Buffer)), "read non-existent chunk should error")

	// Another data.
	secondData, secondID := rd.New(t, 512), uuid.New()
	require.NotEqual(t, id, secondID, "different IDs")
	require.NotEqual(t, data, secondData, "different data")
	require.NoError(t, chunks.Write(ctx, secondID, bytes.NewReader(secondData)), "write")
	buf.Reset()
	require.NoError(t, chunks.Read(ctx, secondID, buf), "read")
	require.Equal(t, secondData, buf.Bytes(), "read data should equal to written data")
}
