package node

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// Register itself on the front node.
func Register(ctx context.Context, httpClient HTTPClient, listenPort string) error {
	lg := zctx.From(ctx)
	lg.Info("Registering node")
	hostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}
	baseURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(hostname, listenPort),
	}
	u := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("front", "8080"),
		Path:   "/register",
		RawQuery: url.Values{
			"baseURL": []string{baseURL.String()},
		}.Encode(),
	}
	req, err := http.NewRequest(http.MethodPut, u.String(), nil)
	if err != nil {
		return errors.Wrap(err, "create request")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	lg.Info("Registered", zap.String("baseURL", baseURL.String()))
	return nil
}
