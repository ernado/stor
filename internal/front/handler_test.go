package front

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	noopMeter "go.opentelemetry.io/otel/metric/noop"
	noopTracer "go.opentelemetry.io/otel/trace/noop"
)

type inMemoryStorage struct {
	files map[string]File
	nodes map[string]Node
	mux   sync.Mutex
}

func (s *inMemoryStorage) NodeStats(ctx context.Context) ([]NodeStat, error) {
	s.mux.Lock()
	defer s.mux.Unlock()
	var stats []NodeStat
	for _, node := range s.nodes {
		stat := NodeStat{
			BaseURL: node.BaseURL,
		}
		for _, file := range s.files {
			for _, chunk := range file.Chunks {
				if chunk.NodeBaseURL == node.BaseURL {
					stat.TotalChunks++
					stat.TotalSize += chunk.Size
				}
			}
		}
		stats = append(stats, stat)
	}
	return stats, nil
}

func (s *inMemoryStorage) File(_ context.Context, name string) (*File, error) {
	s.mux.Lock()
	defer s.mux.Unlock()
	v, ok := s.files[name]
	if !ok {
		return nil, &FileNotFoundErr{File: name}
	}
	return &v, nil
}

func (s *inMemoryStorage) AddFile(_ context.Context, file File) error {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.files[file.Name] = file
	return nil
}

func (s *inMemoryStorage) RemoveFile(_ context.Context, name string) error {
	s.mux.Lock()
	defer s.mux.Unlock()
	delete(s.files, name)
	return nil
}

func (s *inMemoryStorage) Nodes(_ context.Context) ([]Node, error) {
	s.mux.Lock()
	defer s.mux.Unlock()
	var nodes []Node
	for _, node := range s.nodes {
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (s *inMemoryStorage) AddNode(_ context.Context, node Node) error {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.nodes[node.BaseURL] = node
	return nil
}

func newInMemoryStorage() *inMemoryStorage {
	return &inMemoryStorage{
		files: make(map[string]File),
		nodes: make(map[string]Node),
	}
}

type inMemoryNodes struct {
	nodes map[string]*inMemoryNode
}

type inMemoryNode struct {
	baseURL string

	mux    sync.Mutex
	chunks map[uuid.UUID][]byte
}

func (i *inMemoryNode) Read(_ context.Context, chunkID uuid.UUID, w io.Writer) error {
	i.mux.Lock()
	reader := bytes.NewReader(i.chunks[chunkID])
	i.mux.Unlock()
	_, err := io.Copy(w, reader)
	return err
}

func (i *inMemoryNode) Write(_ context.Context, chunkID uuid.UUID, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	i.mux.Lock()
	defer i.mux.Unlock()
	i.chunks[chunkID] = data
	return nil
}

func (i *inMemoryNode) Delete(_ context.Context, id uuid.UUID) error {
	i.mux.Lock()
	defer i.mux.Unlock()
	delete(i.chunks, id)
	return nil
}

func (i *inMemoryNode) BaseURL() string {
	return i.baseURL
}

func (i inMemoryNodes) NewClient(baseURL string) NodeClient {
	return i.nodes[baseURL]
}

func (i inMemoryNodes) createClient(baseURL string) {
	i.nodes[baseURL] = &inMemoryNode{
		baseURL: baseURL,
		chunks:  make(map[uuid.UUID][]byte),
	}
}

func newInMemoryNodes() *inMemoryNodes {
	return &inMemoryNodes{
		nodes: make(map[string]*inMemoryNode),
	}
}

func TestHandler(t *testing.T) {
	var (
		ctx   = context.Background()
		stor  = newInMemoryStorage()
		nodes = newInMemoryNodes()
	)
	handler, err := NewHandler(ctx, nodes, stor, noopTracer.NewTracerProvider(), noopMeter.NewMeterProvider())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	client := server.Client()

	t.Log("Register nodes")
	for _, baseURL := range []string{
		"node1:8080", "node2:8080", "node3:8080", "node4:8080", "node5:8080", "node6:8080",
	} {
		nodes.createClient(baseURL)
		u, err := url.Parse(server.URL)
		require.NoError(t, err)
		u.Path = "/register"
		u.RawQuery = url.Values{
			"baseURL": []string{baseURL},
		}.Encode()
		req, err := http.NewRequest(http.MethodPost, u.String(), http.NoBody)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	t.Log("Upload file")
	var uploadedData []byte
	{
		// Write multipart form.
		b := new(bytes.Buffer)
		mw := multipart.NewWriter(b)
		w, err := mw.CreateFormFile("upload", "hello.txt")
		require.NoError(t, err)

		// Write random file contents.
		rnd := rand.New(rand.NewSource(1))
		data := make([]byte, 1024)
		_, err = rnd.Read(data)
		uploadedData = data
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)

		require.NoError(t, mw.Close())
		req, err := http.NewRequest(http.MethodPost, server.URL+"/upload", b)
		require.NoError(t, err)

		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := client.Do(req)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	{
		// Download file.
		req, err := http.NewRequest(http.MethodGet, server.URL+"/download/hello.txt", http.NoBody)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, resp.Body)

		require.NoError(t, err)
		require.Equal(t, uploadedData, buf.Bytes())
	}
}
