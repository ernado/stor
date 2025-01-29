package front

import (
	"context"
	"path"
	"slices"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/query"
	"github.com/ydb-platform/ydb-go-sdk/v3/table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/options"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/types"
	"go.opentelemetry.io/otel/trace"
)

var _ HandlerStorage = (*YDBStorage)(nil)

func NewYDBStorage(db *ydb.Driver, tracer trace.Tracer) *YDBStorage {
	return &YDBStorage{
		db:     db,
		tracer: tracer,
	}
}

type YDBStorage struct {
	db     *ydb.Driver
	tracer trace.Tracer
}

func (y YDBStorage) NodeStats(ctx context.Context) ([]NodeStat, error) {
	ctx, span := y.tracer.Start(ctx, "meta.NodeStats")
	defer span.End()

	nodes, err := y.Nodes(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get nodes")
	}

	stats := make(map[string]NodeStat)
	for _, node := range nodes {
		stats[node.BaseURL] = NodeStat{
			BaseURL: node.BaseURL,
		}
	}

	if err := y.db.Query().Do(ctx,
		func(ctx context.Context, s query.Session) error {
			res, err := s.Query(ctx,
				`SELECT node, count(1) as total_count, sum(size) as total_size FROM chunks GROUP BY node ORDER BY total_size DESC;`,
			)
			for rs, err := range res.ResultSets(ctx) {
				if err != nil {
					return errors.Wrap(err, "result set")
				}
				for row, err := range rs.Rows(ctx) {
					if err != nil {
						return errors.Wrap(err, "row")
					}
					var v struct {
						Node  string `sql:"node"`
						Count uint64 `sql:"total_count"`
						Size  uint64 `sql:"total_size"`
					}
					if err := row.ScanStruct(&v); err != nil {
						return errors.Wrap(err, "scan")
					}
					stats[v.Node] = NodeStat{
						BaseURL:     v.Node,
						TotalChunks: int(v.Count),
						TotalSize:   int64(v.Size),
					}
				}
			}
			if err != nil {
				return errors.Wrap(err, "query")
			}
			return nil
		},
	); err != nil {
		return nil, errors.Wrap(err, "do")
	}

	var out []NodeStat
	for _, stat := range stats {
		out = append(out, stat)
	}
	slices.SortFunc(out, func(a, b NodeStat) int {
		return int(a.TotalSize - b.TotalSize)
	})

	return out, nil
}

func (y YDBStorage) RemoveFile(ctx context.Context, name string) error {
	ctx, span := y.tracer.Start(ctx, "meta.RemoveFile")
	defer span.End()

	if err := y.db.Table().DoTx(ctx,
		func(ctx context.Context, tx table.TransactionActor) (err error) {
			res, err := tx.Execute(ctx, `DECLARE $fileName AS UTF8;
			DELETE FROM files
			WHERE
			  name = $fileName;`,
				table.NewQueryParameters(
					table.ValueParam("$fileName", types.UTF8Value(name)),
				),
			)
			if err != nil {
				return errors.Wrap(err, "execute")
			}
			if err = res.Err(); err != nil {
				return errors.Wrap(err, "result")
			}
			if err := res.Close(); err != nil {
				return errors.Wrap(err, "close")
			}

			res, err = tx.Execute(ctx, `DECLARE $fileName AS UTF8;
			DELETE FROM chunks
			WHERE
			  file = $fileName;`,
				table.NewQueryParameters(
					table.ValueParam("$fileName", types.UTF8Value(name)),
				),
			)
			if err != nil {
				return errors.Wrap(err, "execute")
			}
			if err = res.Err(); err != nil {
				return errors.Wrap(err, "result")
			}
			if err := res.Close(); err != nil {
				return errors.Wrap(err, "close")
			}

			return nil
		}, table.WithIdempotent(),
	); err != nil {
		return errors.Wrap(err, "delete file")
	}

	return nil
}

func (y YDBStorage) CreateTables(ctx context.Context) error {
	ctx, span := y.tracer.Start(ctx, "meta.CreateTables")
	defer span.End()

	if err := y.db.Table().Do(ctx,
		func(ctx context.Context, s table.Session) (err error) {
			return s.CreateTable(ctx, path.Join(y.db.Name(), "files"),
				options.WithColumn("name", types.TypeUTF8),
				options.WithColumn("size", types.TypeUint64),
				options.WithPrimaryKeyColumn("name"),
			)
		},
	); err != nil {
		return errors.Wrap(err, "create files table")
	}
	if err := y.db.Table().Do(ctx,
		func(ctx context.Context, s table.Session) (err error) {
			return s.CreateTable(ctx, path.Join(y.db.Name(), "chunks"),
				options.WithColumn("file", types.TypeUTF8),
				options.WithColumn("index", types.TypeUint64),
				options.WithColumn("id", types.TypeUUID),
				options.WithColumn("offset", types.TypeUint64),
				options.WithColumn("size", types.TypeUint64),
				options.WithColumn("node", types.TypeUTF8),
				options.WithPrimaryKeyColumn("file", "index"),
			)
		},
	); err != nil {
		return errors.Wrap(err, "create chunks table")
	}
	if err := y.db.Table().Do(ctx,
		func(ctx context.Context, s table.Session) (err error) {
			return s.CreateTable(ctx, path.Join(y.db.Name(), "nodes"),
				options.WithColumn("base_url", types.TypeUTF8),
				options.WithPrimaryKeyColumn("base_url"),
			)
		},
	); err != nil {
		return errors.Wrap(err, "create nodes table")
	}
	return nil
}

type FileNotFoundErr struct {
	File string
}

func (e *FileNotFoundErr) Error() string {
	return "file not found: " + e.File
}

type ChunksNotFound struct {
	File string
}

func (e *ChunksNotFound) Error() string {
	return "chunks not found: " + e.File
}

func (y YDBStorage) File(ctx context.Context, name string) (*File, error) {
	ctx, span := y.tracer.Start(ctx, "meta.File")
	defer span.End()

	// Fetch file from YDB.
	var file File
	if err := y.db.Query().Do(ctx,
		func(ctx context.Context, s query.Session) error {
			res, err := s.Query(ctx,
				`DECLARE $fileName AS UTF8;
			SELECT
			  name,
			  size,
			FROM
			  files
			WHERE
			  name = $fileName;`,
				query.WithParameters(
					table.NewQueryParameters(
						table.ValueParam("$fileName", types.UTF8Value(name)),
					),
				),
			)

			for rs, err := range res.ResultSets(ctx) {
				if err != nil {
					return errors.Wrap(err, "result set")
				}
				for row, err := range rs.Rows(ctx) {
					if err != nil {
						return errors.Wrap(err, "row")
					}
					var v struct {
						Name string `sql:"name"`
						Size uint64 `sql:"size"`
					}
					if err := row.ScanStruct(&v); err != nil {
						return errors.Wrap(err, "scan")
					}
					file.Name = v.Name
					file.Size = int64(v.Size)
				}
			}
			if err != nil {
				return errors.Wrap(err, "query")
			}
			return nil
		},
	); err != nil {
		return nil, errors.Wrap(err, "query")
	}

	if file.Name == "" {
		return nil, &FileNotFoundErr{File: name}
	}

	if err := y.db.Query().Do(ctx,
		func(ctx context.Context, s query.Session) error {
			res, err := s.Query(ctx,
				`DECLARE $fileName AS UTF8;
			SELECT
			  index,
              id,
			  offset,
			  size,
			  node
			FROM
			  chunks
			WHERE
			  file = $fileName;`,
				query.WithParameters(
					table.NewQueryParameters(
						table.ValueParam("$fileName", types.UTF8Value(name)),
					),
				),
			)

			for rs, err := range res.ResultSets(ctx) {
				if err != nil {
					return errors.Wrap(err, "result set")
				}
				for row, err := range rs.Rows(ctx) {
					if err != nil {
						return errors.Wrap(err, "row")
					}
					var v struct {
						Index  uint64    `sql:"index"`
						ID     uuid.UUID `sql:"id"`
						Offset uint64    `sql:"offset"`
						Size   uint64    `sql:"size"`
						Node   string    `sql:"node"`
					}
					if err := row.ScanStruct(&v); err != nil {
						return errors.Wrap(err, "scan")
					}
					file.Chunks = append(file.Chunks, Chunk{
						Index:       int(v.Index),
						ID:          v.ID,
						Offset:      int64(v.Offset),
						Size:        int64(v.Size),
						NodeBaseURL: v.Node,
					})
				}
			}
			if err != nil {
				return errors.Wrap(err, "query")
			}
			return nil
		},
	); err != nil {
		return nil, errors.Wrap(err, "do")
	}
	if len(file.Chunks) == 0 {
		return nil, &ChunksNotFound{File: name}
	}

	return &file, nil
}

func (y YDBStorage) AddFile(ctx context.Context, file File) error {
	ctx, span := y.tracer.Start(ctx, "meta.AddFile")
	defer span.End()

	if err := y.db.Table().DoTx( // Do retry operation on errors with best effort
		ctx, // context manages exiting from Do
		func(ctx context.Context, tx table.TransactionActor) (err error) { // retry operation
			res, err := tx.Execute(ctx, `
          DECLARE $name AS UTF8;
          DECLARE $size AS UInt64;
          UPSERT INTO files ( name, size )
          VALUES ( $name, $size );
        `,
				table.NewQueryParameters(
					table.ValueParam("$name", types.UTF8Value(file.Name)),
					table.ValueParam("$size", types.Uint64Value(uint64(file.Size))),
				),
			)
			if err != nil {
				return errors.Wrap(err, "execute")
			}
			if err = res.Err(); err != nil {
				return errors.Wrap(err, "result")
			}
			if err := res.Close(); err != nil {
				return errors.Wrap(err, "close")
			}

			for _, chunk := range file.Chunks {
				res, err = tx.Execute(ctx, `
		  DECLARE $file AS UTF8;
		  DECLARE $index AS UInt64;
		  DECLARE $id AS UUID;
		  DECLARE $offset AS UInt64;
		  DECLARE $size AS UInt64;
		  DECLARE $node AS UTF8;
		  UPSERT INTO chunks ( file, index, id, offset, size, node )
		  VALUES ( $file, $index, $id, $offset, $size, $node );
		`,
					table.NewQueryParameters(
						table.ValueParam("$file", types.UTF8Value(file.Name)),
						table.ValueParam("$index", types.Uint64Value(uint64(chunk.Index))),
						table.ValueParam("$id", types.UuidValue(chunk.ID)),
						table.ValueParam("$offset", types.Uint64Value(uint64(chunk.Offset))),
						table.ValueParam("$size", types.Uint64Value(uint64(chunk.Size))),
						table.ValueParam("$node", types.UTF8Value(chunk.NodeBaseURL)),
					),
				)
				if err != nil {
					return errors.Wrap(err, "execute")
				}
				if err = res.Err(); err != nil {
					return errors.Wrap(err, "result")
				}
				if err := res.Close(); err != nil {
					return errors.Wrap(err, "close")
				}
			}

			return nil
		}, table.WithIdempotent(),
	); err != nil {
		return errors.Wrap(err, "upsert file")
	}
	return nil
}

func (y YDBStorage) Nodes(ctx context.Context) ([]Node, error) {
	ctx, span := y.tracer.Start(ctx, "meta.Nodes")
	defer span.End()

	var nodes []Node
	if err := y.db.Query().Do(ctx,
		func(ctx context.Context, s query.Session) error {
			res, err := s.Query(ctx, `SELECT base_url FROM nodes;`)
			for rs, err := range res.ResultSets(ctx) {
				if err != nil {
					return errors.Wrap(err, "result set")
				}
				for row, err := range rs.Rows(ctx) {
					if err != nil {
						return errors.Wrap(err, "row")
					}
					var node Node
					if err := row.Scan(&node.BaseURL); err != nil {
						return errors.Wrap(err, "scan")
					}
					nodes = append(nodes, node)
				}
			}
			if err != nil {
				return errors.Wrap(err, "query")
			}
			return nil
		},
	); err != nil {
		return nil, errors.Wrap(err, "do")
	}

	return nodes, nil
}

func (y YDBStorage) AddNode(ctx context.Context, node Node) error {
	ctx, span := y.tracer.Start(ctx, "meta.AddNode")
	defer span.End()

	if err := y.db.Table().DoTx(ctx,
		func(ctx context.Context, tx table.TransactionActor) (err error) {
			res, err := tx.Execute(ctx, `
          DECLARE $base_url AS UTF8;
          UPSERT INTO nodes ( base_url )
          VALUES ( $base_url );
        `,
				table.NewQueryParameters(
					table.ValueParam("$base_url", types.UTF8Value(node.BaseURL)),
				),
			)
			if err != nil {
				return errors.Wrap(err, "execute")
			}
			if err = res.Err(); err != nil {
				return errors.Wrap(err, "result")
			}
			if err := res.Close(); err != nil {
				return errors.Wrap(err, "close")
			}

			return nil
		}, table.WithIdempotent(),
	); err != nil {
		return errors.Wrap(err, "upsert node")
	}

	return nil
}
