package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	noopTracer "go.opentelemetry.io/otel/trace/noop"
)

type inMemoryChunks struct {
	chunks map[uuid.UUID][]byte
}

func (c *inMemoryChunks) Write(_ context.Context, id uuid.UUID, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	c.chunks[id] = data
	return nil
}

func (c *inMemoryChunks) Read(_ context.Context, id uuid.UUID, w io.Writer) error {
	data, ok := c.chunks[id]
	if !ok {
		return errors.New("not found")
	}
	_, err := w.Write(data)
	return err
}

func newInMemoryChunks() *inMemoryChunks {
	return &inMemoryChunks{
		chunks: make(map[uuid.UUID][]byte),
	}
}

func TestNewHandler(t *testing.T) {
	var (
		storage = newInMemoryChunks()
		handler = NewHandler(storage)
		server  = httptest.NewServer(handler)
		client  = NewClient(server.URL, server.Client(), noopTracer.NewTracerProvider())
		rd      = newRandomData()
		data    = rd.New(t, 1024)
		ctx     = context.Background()
		id      = uuid.New()
	)
	require.NoError(t, client.Write(ctx, id, bytes.NewReader(data)), "write")
	buf := new(bytes.Buffer)
	require.NoError(t, client.Read(ctx, id, buf), "read")
	require.Equal(t, data, buf.Bytes(), "read data should equal to written data")
	require.Equal(t, data, storage.chunks[id], "read data should equal to storage data")

	// Another data.
	secondData, secondID := rd.New(t, 512), uuid.New()
	require.NotEqual(t, id, secondID, "different IDs")
	require.NotEqual(t, data, secondData, "different data")

	require.NoError(t, client.Write(ctx, secondID, bytes.NewReader(secondData)), "write")
	buf.Reset()
	require.NoError(t, client.Read(ctx, secondID, buf), "read")
	require.Equal(t, secondData, buf.Bytes(), "read data should equal to written data")
}
