package node

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type Chunks struct {
	dir string

	trace      trace.Tracer
	bytesRead  metric.Int64Counter
	bytesWrote metric.Int64Counter
}

func NewChunks(dir string, tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider) (*Chunks, error) {
	const name = "stor.node"

	meter := meterProvider.Meter(name)
	bytesRead, err := meter.Int64Counter("node.bytes.read")
	if err != nil {
		return nil, errors.Wrap(err, "bytes read")
	}
	bytesWrote, err := meter.Int64Counter("node.bytes.wrote")
	if err != nil {
		return nil, errors.Wrap(err, "bytes wrote")
	}

	return &Chunks{
		dir: dir,

		trace:      tracerProvider.Tracer(name),
		bytesRead:  bytesRead,
		bytesWrote: bytesWrote,
	}, nil
}

func getTargetDir(dir string, id uuid.UUID) string {
	idStr := id.String()
	return filepath.Join(dir, idStr[0:2], idStr[2:4])
}

// Write chunk to disk.
func (c *Chunks) Write(ctx context.Context, id uuid.UUID, r io.Reader) (rerr error) {
	_, span := c.trace.Start(ctx, "Chunks.Write")
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
		}
		span.End()
	}()

	targetDir := getTargetDir(c.dir, id)
	const dirPerm = 0o755
	if err := os.MkdirAll(targetDir, dirPerm); err != nil {
		return errors.Wrap(err, "mkdir")
	}

	f, err := os.Create(filepath.Join(targetDir, id.String()))
	if err != nil {
		return errors.Wrap(err, "create")
	}
	defer func() {
		_ = f.Close()
		if rerr != nil {
			// Cleanup failed chunk.
			if deleteErr := os.Remove(f.Name()); deleteErr != nil {
				zctx.From(ctx).Warn("Failed to delete chunk",
					zap.Error(deleteErr),
				)
			}
		}
	}()

	n, err := io.Copy(f, r)
	c.bytesWrote.Add(ctx, n)
	if err != nil {
		return errors.Wrap(err, "copy")
	}

	if err := f.Close(); err != nil {
		return errors.Wrap(err, "close")
	}

	return nil
}

func (c *Chunks) Read(ctx context.Context, id uuid.UUID, w io.Writer) (rerr error) {
	_, span := c.trace.Start(ctx, "Chunks.Read")
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
		}
		span.End()
	}()

	filePath := filepath.Join(getTargetDir(c.dir, id), id.String())
	f, err := os.Open(filePath)
	if err != nil {
		return errors.Wrap(err, "open")
	}
	defer func() { _ = f.Close() }()

	n, err := io.Copy(w, f)
	c.bytesRead.Add(ctx, n)
	if err != nil {
		return errors.Wrap(err, "copy")
	}

	return nil
}
