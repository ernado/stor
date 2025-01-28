package front

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/ernado/stor/internal/node"
)

type Chunk struct {
	Index  int
	ID     uuid.UUID
	Offset int64
	Size   int64
	Client *node.Client
}

type File struct {
	Size   int64
	Name   string
	Chunks []Chunk
}

type Handler struct {
	mux     sync.Mutex
	clients map[string]*node.Client
	files   map[string]File

	chunksPerFile          int
	maxMultipartFormMemory int64
}

func (h *Handler) NextClient() *node.Client {
	h.mux.Lock()
	defer h.mux.Unlock()
	for _, client := range h.clients {
		return client // random client
	}
	return nil
}

type limitReaderFrom struct {
	r      io.ReaderAt
	n      int64
	offset int64
}

func (l *limitReaderFrom) Read(p []byte) (n int, err error) {
	if l.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err = l.r.ReadAt(p, l.offset)
	l.offset += int64(n)
	l.n -= int64(n)
	return
}

func NewHandler(baseCtx context.Context, httpClient node.HTTPClient, tracerProvider trace.TracerProvider) http.Handler {
	h := &Handler{
		clients:                make(map[string]*node.Client),
		files:                  make(map[string]File),
		maxMultipartFormMemory: 32 * 1024 * 1024,
		chunksPerFile:          6,
	}
	tracer := tracerProvider.Tracer("stor.front")
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "Register")
		defer span.End()

		// Register new node.
		// Use node.Client to communicate with the node.
		// Store node.Client in h.clients.
		baseURL := r.URL.Query().Get("baseURL")
		if baseURL == "" {
			http.Error(w, "baseURL is required", http.StatusBadRequest)
			return
		}

		client := node.NewClient(baseURL, httpClient, tracerProvider)
		h.mux.Lock()
		h.clients[baseURL] = client
		h.mux.Unlock()

		zctx.From(ctx).Info("Registered node",
			zap.String("baseURL", baseURL),
		)
	})
	mux.HandleFunc("/download/{fileName}", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "Download")
		defer span.End()

		fileName := r.PathValue("fileName")
		if fileName == "" {
			http.Error(w, "fileName is required", http.StatusBadRequest)
			return
		}

		h.mux.Lock()
		file, ok := h.files[fileName]
		h.mux.Unlock()

		if !ok {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		// Set Content-Length header.
		w.Header().Set("Content-Length", fmt.Sprint(file.Size))

		// Read chunks continuously.
		for _, chunk := range file.Chunks {
			if err := chunk.Client.Read(ctx, chunk.ID, w); err != nil {
				// Failed.
				span.RecordError(err,
					trace.WithAttributes(
						attribute.Int("chunkIndex", chunk.Index),
						attribute.String("chunkID", chunk.ID.String()),
					),
				)
				return
			}
		}

		// Success.
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx, span := tracer.Start(r.Context(), "Upload")
		defer span.End()

		if err := r.ParseMultipartForm(h.maxMultipartFormMemory); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var formKey string
		for k := range r.MultipartForm.File {
			formKey = k
			break
		}
		if formKey == "" {
			http.Error(w, "file is required", http.StatusBadRequest)
			return
		}
		zctx.From(ctx).Info("Selected file from form", zap.String("formKey", formKey))
		formFile, fileHeader, err := r.FormFile(formKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Save MIME type, file name, and file size.
		// Split file into N chunks.
		// Delete uploaded chunks if user cancels upload.

		size := fileHeader.Size
		// Split file into N chunks.
		chunkSize := size / int64(h.chunksPerFile)
		span.AddEvent("Splitting file into chunks",
			trace.WithAttributes(
				attribute.String("formKey", formKey),
				attribute.String("fileName", fileHeader.Filename),
				attribute.Int("chunksPerFile", h.chunksPerFile),
				attribute.Int64("chunkSize", chunkSize),
			),
		)
		chunks := make([]Chunk, h.chunksPerFile)
		for i := 0; i < h.chunksPerFile; i++ {
			client := h.NextClient()
			if client == nil {
				http.Error(w, "no client", http.StatusInternalServerError)
				return
			}
			chunks[i] = Chunk{
				Index:  i,
				ID:     uuid.New(),
				Offset: int64(i) * chunkSize,
				Size:   chunkSize,
				Client: client,
			}
			if i == h.chunksPerFile-1 {
				// Last chunk.
				chunks[i].Size = size - chunks[i].Offset
			}
		}

		g, ctx := errgroup.WithContext(ctx)
		for _, chunk := range chunks {
			g.Go(func() error {
				return chunk.Client.Write(ctx, chunk.ID, &limitReaderFrom{
					r:      formFile,
					n:      chunk.Size,
					offset: chunk.Offset,
				})
			})
		}
		if err := g.Wait(); err != nil {
			// Remove uploaded chunks.
			for _, chunk := range chunks {
				// Start new span with link to the previous span.
				link := trace.LinkFromContext(ctx)
				// Use baseCtx as ctx can be already canceled.
				ctx, span = tracer.Start(baseCtx, "DeleteChunk")
				span.AddLink(link)
				if err := chunk.Client.Delete(ctx, chunk.ID); err != nil {
					zctx.From(ctx).Warn("Failed to delete chunk",
						zap.String("chunkID", chunk.ID.String()),
						zap.Error(err),
					)
				}
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Return uploaded file link.
		// Assume that we are on 127.0.0.1.
		u := &url.URL{
			Scheme: "http",
			Host:   r.Host,
			Path:   filepath.Join("/download", fileHeader.Filename),
		}
		w.WriteHeader(http.StatusOK)
		h.mux.Lock()
		h.files[fileHeader.Filename] = File{
			Size:   size,
			Name:   fileHeader.Filename,
			Chunks: chunks,
		}
		h.mux.Unlock()
		fmt.Fprintln(w, u.String())
	})
	return mux
}
