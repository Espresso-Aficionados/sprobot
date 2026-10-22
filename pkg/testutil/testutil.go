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
	// Fail makes every request for a path fail with the given status code.
	// 404 answers NoSuchKey. Use a 4xx code: the SDK retries 5xx with backoff.
	Fail map[string]int
	// Deleted lists the path of every DELETE request that succeeded, in order.
	Deleted []string
}

func NewFakeS3() *FakeS3 {
	return &FakeS3{Objects: make(map[string][]byte), Fail: make(map[string]int)}
}

func (f *FakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	key := r.URL.Path

	if status, ok := f.Fail[key]; ok {
		code := "InjectedError"
		if status == http.StatusNotFound {
			code = "NoSuchKey"
		}
		writeError(w, status, code)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, ok := f.Objects[key]
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case http.MethodHead:
		data, ok := f.Objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.Objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		// Like S3, deleting a missing key succeeds.
		delete(f.Objects, key)
		f.Deleted = append(f.Deleted, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, http.StatusText(status))
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
