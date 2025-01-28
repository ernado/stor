package node

import (
	"context"
	"io"
	"net/http"

	"github.com/google/uuid"
)

type HandlerStorage interface {
	Read(ctx context.Context, id uuid.UUID, w io.Writer) error
	Write(ctx context.Context, id uuid.UUID, r io.Reader) error
}

type Handler struct {
	storage HandlerStorage
}

func NewHandler(storage HandlerStorage) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			if err := storage.Read(ctx, id, w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case http.MethodPut:
			if err := storage.Write(ctx, id, r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}
