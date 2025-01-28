package front

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/ernado/stor/internal/node"
)

type Chunk struct {
	Index       int
	ID          uuid.UUID
	Offset      int64
	Size        int64
	NodeBaseURL string // [Node.BaseURL]
}

type File struct {
	Size   int64
	Name   string
	Chunks []Chunk
}

type Node struct {
	BaseURL string
}

type HandlerStorage interface {
	File(ctx context.Context, name string) (*File, error)
	AddFile(ctx context.Context, file File) error
	RemoveFile(ctx context.Context, name string) error
	Nodes(ctx context.Context) ([]Node, error)
	AddNode(ctx context.Context, node Node) error
}

type Handler struct {
	mux     sync.Mutex
	clients map[string]*node.Client

	storage                HandlerStorage
	chunksPerFile          int
	maxMultipartFormMemory int64
	tracerProvider         trace.TracerProvider
	httpClient             node.HTTPClient
	tracer                 trace.Tracer
	baseCtx                context.Context
}

func (h *Handler) FetchNodes(ctx context.Context) error {
	ctx, span := h.tracer.Start(ctx, "handler.FetchNodes")
	defer span.End()

	clients, err := h.storage.Nodes(ctx)
	if err != nil {
		return errors.Wrap(err, "fetch clients")
	}

	h.mux.Lock()
	defer h.mux.Unlock()

	for _, client := range clients {
		h.clients[client.BaseURL] = h.newClient(client.BaseURL)
	}

	return nil
}

// NextClient returns next client to use for uploads.
func (h *Handler) NextClient() *node.Client {
	h.mux.Lock()
	defer h.mux.Unlock()

	for _, client := range h.clients {
		return client
	}

	return nil
}

// GetClient creates or returns existing client to baseURL.
func (h *Handler) GetClient(baseURL string) *node.Client {
	h.mux.Lock()
	defer h.mux.Unlock()

	client, ok := h.clients[baseURL]
	if !ok {
		client = h.newClient(baseURL)
		h.clients[baseURL] = client
	}

	return client
}

func (h *Handler) newClient(baseURL string) *node.Client {
	return node.NewClient(baseURL, h.httpClient, h.tracerProvider)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.Register")
	defer span.End()
	baseURL := r.URL.Query().Get("baseURL")
	if baseURL == "" {
		http.Error(w, "baseURL is required", http.StatusBadRequest)
		return
	}
	if err := h.storage.AddNode(ctx, Node{BaseURL: baseURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	zctx.From(ctx).Info("Registered node",
		zap.String("baseURL", baseURL),
	)
	if err := h.FetchNodes(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.Download")
	defer span.End()

	fileName := r.PathValue("fileName")
	if fileName == "" {
		http.Error(w, "fileName is required", http.StatusBadRequest)
		return
	}
	file, err := h.storage.File(ctx, fileName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprint(file.Size))

	// Read chunks continuously.
	for _, chunk := range file.Chunks {
		client := h.GetClient(chunk.NodeBaseURL)

		if err := client.Read(ctx, chunk.ID, w); err != nil {
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
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx, span := h.tracer.Start(r.Context(), "handler.Upload")
	defer span.End()

	if err := h.FetchNodes(ctx); err != nil {
		// Can be done in background.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

	// Split file into N chunks.
	size := fileHeader.Size
	chunkSize := size / int64(h.chunksPerFile)
	span.AddEvent("Splitting file into chunks",
		trace.WithAttributes(
			attribute.String("formKey", formKey),
			attribute.String("fileName", fileHeader.Filename),
			attribute.Int("chunksPerFile", h.chunksPerFile),
			attribute.Int64("chunkSize", chunkSize),
		),
	)
	// Prepare chunks and allocate clients to storage nodes.
	chunks := make([]Chunk, h.chunksPerFile)
	for i := 0; i < h.chunksPerFile; i++ {
		client := h.NextClient()
		if client == nil {
			http.Error(w, "no client", http.StatusInternalServerError)
			return
		}
		chunks[i] = Chunk{
			Index:       i,
			ID:          uuid.New(),
			Offset:      int64(i) * chunkSize,
			Size:        chunkSize,
			NodeBaseURL: client.BaseURL(),
		}
		if i == h.chunksPerFile-1 {
			// Last chunk.
			chunks[i].Size = size - chunks[i].Offset
		}
	}

	// Upload and save metadata concurrently.
	g, ctx := errgroup.WithContext(ctx)
	for _, chunk := range chunks {
		client := h.GetClient(chunk.NodeBaseURL)

		g.Go(func() error {
			return client.Write(ctx, chunk.ID, &LimitReaderFrom{
				R:      formFile,
				N:      chunk.Size,
				Offset: chunk.Offset,
			})
		})
	}
	g.Go(func() error {
		// Add file to metadata storage.
		file := File{
			Size:   size,
			Name:   fileHeader.Filename,
			Chunks: chunks,
		}
		return h.storage.AddFile(ctx, file)
	})
	if err := g.Wait(); err != nil {
		// Remove uploaded chunks.
		link := trace.LinkFromContext(ctx)
		// Use baseCtx as ctx can be already canceled.
		ctx, span = h.tracer.Start(h.baseCtx, "Cleanup")
		span.AddLink(link)
		defer span.End()

		for _, chunk := range chunks {
			// Start new span with link to the previous span.
			client := h.GetClient(chunk.NodeBaseURL)

			if err := client.Delete(ctx, chunk.ID); err != nil {
				zctx.From(ctx).Warn("Failed to delete chunk",
					zap.String("chunkID", chunk.ID.String()),
					zap.Error(err),
				)
			}
		}
		if err := h.storage.RemoveFile(ctx, fileHeader.Filename); err != nil {
			zctx.From(ctx).Warn("Failed to remove file",
				zap.String("fileName", fileHeader.Filename),
				zap.Error(err),
			)
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
	fmt.Fprintln(w, u.String())
}

func NewHandler(
	baseCtx context.Context,
	httpClient node.HTTPClient,
	storage HandlerStorage,
	tracerProvider trace.TracerProvider,
) http.Handler {
	h := &Handler{
		storage:                storage,
		maxMultipartFormMemory: 32 * 1024 * 1024,
		chunksPerFile:          6,
		httpClient:             httpClient,
		tracerProvider:         tracerProvider,
		tracer:                 tracerProvider.Tracer("stor.front"),
		baseCtx:                baseCtx,
		clients:                make(map[string]*node.Client),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/register", h.register)
	mux.HandleFunc("/download/{fileName}", h.download)
	mux.HandleFunc("/upload", h.upload)
	return mux
}
