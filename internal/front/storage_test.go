package front

import (
	"context"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/balancers"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/ernado/stor/internal/integration"
)

func newYDB(t *testing.T) *ydb.Driver {
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Name:         "stor-ydb",
		Image:        "ydbplatform/local-ydb:latest",
		ExposedPorts: []string{"2136/tcp"},
		Hostname:     "localhost",
	}
	ydbContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
		Logger:           testcontainers.TestLogger(t),
		Reuse:            true,
	})
	require.NoError(t, err, "container start")

	endpoint, err := ydbContainer.PortEndpoint(ctx, "2136", "grpc")
	require.NoError(t, err, "container endpoint")

	connectBackoff := backoff.NewExponentialBackOff()
	connectBackoff.InitialInterval = 100 * time.Millisecond
	connectBackoff.MaxElapsedTime = time.Minute
	connectBackoff.MaxInterval = 1 * time.Second

	db, err := backoff.RetryNotifyWithData(func() (*ydb.Driver, error) {
		return ydb.Open(ctx, endpoint+"/local",
			ydb.WithBalancer(balancers.SingleConn()), // Hack for local development.
		)
	}, connectBackoff, func(err error, duration time.Duration) {
		t.Logf("retrying in %s: %v", duration, err)
	})
	require.NoError(t, err, "open ydb")
	t.Cleanup(func() {
		require.NoError(t, db.Close(ctx), "close ydb")
	})
	return db
}

func TestIntegrationYDBStorage(t *testing.T) {
	integration.Skip(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	timeout := 10 * time.Second
	storage := YDBStorage{
		db:     newYDB(t),
		tracer: noop.NewTracerProvider().Tracer(""),
	}
	{
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		t.Log("Creating tables")
		require.NoError(t, storage.CreateTables(ctx))
	}
	{
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		t.Log("Inserting nodes")
		nodesURLs := []string{
			"http://localhost:8080",
			"http://localhost:8081",
			"http://localhost:8082",
		}
		for _, node := range nodesURLs {
			require.NoError(t, storage.AddNode(ctx, Node{BaseURL: node}))
		}

		nodes, err := storage.Nodes(ctx)
		require.NoError(t, err, "fetch nodes")
		require.Len(t, nodes, 3)
		for i, node := range nodes {
			require.Contains(t, nodesURLs, node.BaseURL, "node %d", i)
		}
	}
	{
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		t.Log("Inserting files")
		files := []File{
			{
				Name: "file1",
				Chunks: []Chunk{
					{
						NodeBaseURL: "http://localhost:8080",
						Index:       0,
						ID:          uuid.New(),
						Offset:      0,
						Size:        1024,
					},
					{
						NodeBaseURL: "http://localhost:8081",
						Index:       1,
						ID:          uuid.New(),
						Offset:      1024,
						Size:        1024,
					},
				},
			},
		}
		for _, file := range files {
			require.NoError(t, storage.AddFile(ctx, file))
		}
		stats, err := storage.NodeStats(ctx)
		require.NoError(t, err, "fetch node stats")
		require.Len(t, stats, 3)
		for _, file := range files {
			f, err := storage.File(ctx, file.Name)
			require.NoError(t, err)
			require.Equal(t, file, *f)

			require.NoError(t, storage.RemoveFile(ctx, file.Name))
			_, err = storage.File(ctx, file.Name)
			require.Error(t, err)
			var nf *FileNotFoundErr
			require.ErrorAs(t, err, &nf)
		}
	}
}
