package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

func run() error {
	var arg struct {
		File         string
		Name         string
		ServerURL    string
		Check        bool
		RandomPrefix bool
		Generate     bool
		GenerateSize string
	}
	flag.StringVar(&arg.File, "file", "", "file to upload")
	flag.StringVar(&arg.Name, "name", "", "name of the file (defaults to file base name)")
	flag.StringVar(&arg.ServerURL, "server-url", "http://localhost:8080", "server URL")
	flag.BoolVar(&arg.Check, "check", false, "download and check file checksum")
	flag.BoolVar(&arg.RandomPrefix, "rnd", false, "use random prefix for the file name")
	flag.StringVar(&arg.GenerateSize, "gen-size", "100M", "generate file of given size")
	flag.BoolVar(&arg.Generate, "gen", false, "generate random file to temp dir")
	flag.Parse()

	if arg.Generate {
		// Generate random file with specified size.
		f, err := os.CreateTemp("", "stor-upload-*.bin")
		if err != nil {
			return errors.Wrap(err, "create temp file")
		}
		defer func() {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}()
		arg.File = f.Name()
		sizeBytes, err := humanize.ParseBytes(arg.GenerateSize)
		if err != nil {
			return errors.Wrap(err, "parse size")
		}
		// Fixed seed for reproducibility.
		rnd := rand.New(rand.NewSource(0)) // #nosec G404
		if _, err := io.CopyN(f, rnd, int64(sizeBytes)); err != nil {
			return errors.Wrap(err, "generate file")
		}
	}
	if arg.File == "" {
		return errors.New("file is required")
	}

	ctx := context.Background()
	f, err := os.Open(arg.File)
	if err != nil {
		return errors.Wrap(err, "open file")
	}
	defer func() { _ = f.Close() }()
	name := arg.Name
	if name == "" {
		name = filepath.Base(f.Name())
	}
	if arg.RandomPrefix {
		name = fmt.Sprintf("%s-%s", uuid.New().String()[:6], name)
	}

	stat, err := f.Stat()
	if err != nil {
		return errors.Wrap(err, "stat file")
	}
	var uploadedLink string
	{
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Using a pipe to stream multipart form to server.
		r, w := io.Pipe()
		g, gCtx := errgroup.WithContext(ctx)
		req, err := http.NewRequestWithContext(gCtx, http.MethodPost, arg.ServerURL+"/upload", r)
		if err != nil {
			return errors.Wrap(err, "create request")
		}
		mw := multipart.NewWriter(w)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		bar := progressbar.DefaultBytes(stat.Size(), "uploading")
		g.Go(func() error {
			defer func() { _ = w.Close() }()
			part, err := mw.CreateFormFile("upload", name)
			if err != nil {
				return errors.Wrap(err, "create form file")
			}
			if _, err := io.Copy(io.MultiWriter(part, bar), f); err != nil {
				return errors.Wrap(err, "copy file")
			}
			if err := mw.Close(); err != nil {
				return errors.Wrap(err, "close multipart writer")
			}
			return nil
		})
		done := make(chan struct{})
		g.Go(func() error {
			select {
			case <-done:
				_ = bar.Close()
				return nil
			case <-ctx.Done():
				fmt.Println("cancel context?", ctx.Err())
				cancel()
			}
			return nil
		})
		g.Go(func() error {
			defer func() {
				close(done)
			}()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return errors.Wrap(err, "do request")
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return errors.Errorf("unexpected status code: %d", resp.StatusCode)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return errors.Wrap(err, "read response")
			}
			uploadedLink = strings.TrimSpace(string(data))
			fmt.Println("uploaded link:", uploadedLink)
			return nil
		})
		if err := g.Wait(); err != nil {
			return errors.Wrap(err, "upload file")
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !arg.Check {
		return nil
	}

	// Compute sha256(original).
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.Wrap(err, "seek file")
	}
	h := sha256.New()
	bar := progressbar.DefaultBytes(stat.Size(), "computing original sha256")
	if _, err := io.Copy(io.MultiWriter(h, bar), f); err != nil {
		return errors.Wrap(err, "compute checksum")
	}
	originalSHA256 := h.Sum(nil)
	fmt.Printf("original sha256: %x\n", originalSHA256)

	// Compute sha256(downloaded).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uploadedLink, http.NoBody)
	if err != nil {
		return errors.Wrap(err, "create download request")
	}
	bar = progressbar.DefaultBytes(stat.Size(), "downloading")
	defer func() {
		_ = bar.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "do download request")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	h.Reset()
	if _, err := io.Copy(io.MultiWriter(h, bar), resp.Body); err != nil {
		return errors.Wrap(err, "compute downloaded checksum")
	}
	downloadedSHA256 := h.Sum(nil)
	fmt.Printf("downloaded sha256: %x\n", downloadedSHA256)

	// Comparing checksums.
	if !hmac.Equal(originalSHA256, downloadedSHA256) {
		return errors.New("checksum mismatch")
	} else {
		fmt.Println("checksum match")
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %+v\n", err)
		os.Exit(2)
	}
}
