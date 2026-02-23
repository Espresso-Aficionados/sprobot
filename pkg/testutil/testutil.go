package testutil

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// FakeS3 is an in-memory S3-compatible server for testing.
type FakeS3 struct {
	Mu      sync.Mutex
	Objects map[string][]byte
}

func NewFakeS3() *FakeS3 {
	return &FakeS3{Objects: make(map[string][]byte)}
}

func (f *FakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	key := r.URL.Path

	switch r.Method {
	case http.MethodGet:
		data, ok := f.Objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>Not found</Message></Error>`)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.Objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.Objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func NewTestS3Client(t *testing.T, server *httptest.Server) *s3client.Client {
	t.Helper()
	endpoint := server.URL
	bucket := "test-bucket"

	cache, err := lru.New[string, map[string]string](500)
	if err != nil {
		t.Fatalf("creating cache: %v", err)
	}

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: &endpoint,
		Credentials:  credentials.NewStaticCredentialsProvider("key", "secret", ""),
		UsePathStyle: true,
	})

	return s3client.NewDirect(client, bucket, endpoint, cache, DiscardLogger())
}

func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
