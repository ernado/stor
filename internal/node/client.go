package node

import (
	"context"
	"io"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type HTTPClient interface {
	Do(r *http.Request) (*http.Response, error)
}

type Client struct {
	baseURL string
	trace   trace.Tracer
	http    HTTPClient
}

func NewClient(baseURL string, http HTTPClient, tracerProvider trace.TracerProvider) *Client {
	return &Client{
		http:    http,
		baseURL: baseURL,
		trace:   tracerProvider.Tracer("stor.node.client"),
	}
}

func (c *Client) Write(ctx context.Context, id uuid.UUID, r io.Reader) (rerr error) {
	ctx, span := c.trace.Start(ctx, "Write")
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/chunks/"+id.String(), r)
	if err != nil {
		return errors.Wrap(err, "create request")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) Read(ctx context.Context, id uuid.UUID, w io.Writer) (rerr error) {
	ctx, span := c.trace.Start(ctx, "Read")
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/chunks/"+id.String(), nil)
	if err != nil {
		return errors.Wrap(err, "create request")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return errors.Wrap(err, "copy body")
	}

	return nil
}
