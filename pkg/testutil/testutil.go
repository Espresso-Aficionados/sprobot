package testutil

import (
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// FakeS3 is an in-memory S3-compatible server for testing.
type FakeS3 struct {
	Mu      sync.Mutex
	Objects map[string][]byte
	// Modified is each object's last-modified time, set by PUT and copy.
	// Objects added straight to Objects have none and list without one.
	Modified map[string]time.Time
	// Fail makes every request for a path fail with the given status code.
	// 404 answers NoSuchKey. Use a 4xx code: the SDK retries 5xx with backoff.
	// A bucket listing's path is the bucket, e.g. "/test-bucket".
	Fail map[string]int
	// Deleted lists the path of every DELETE request that succeeded, in order.
	Deleted []string
	// Requests lists every request as "METHOD path", in order.
	Requests []string
	// PageSize caps the keys in one listing page; 0 means S3's 1000.
	PageSize int
}

func NewFakeS3() *FakeS3 {
	return &FakeS3{Objects: make(map[string][]byte), Modified: make(map[string]time.Time), Fail: make(map[string]int)}
}

func (f *FakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	key := r.URL.Path
	f.Requests = append(f.Requests, r.Method+" "+key)

	if status, ok := f.Fail[key]; ok {
		code := "InjectedError"
		if status == http.StatusNotFound {
			code = "NoSuchKey"
		}
		writeError(w, status, code)
		return
	}

	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.list(w, r)
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
		if src := r.Header.Get("X-Amz-Copy-Source"); src != "" {
			f.copyObject(w, key, src)
			return
		}
		data, _ := io.ReadAll(r.Body)
		f.Objects[key] = data
		f.touch(key)
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		// Like S3, deleting a missing key succeeds.
		delete(f.Objects, key)
		delete(f.Modified, key)
		f.Deleted = append(f.Deleted, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// copyObject answers CopyObject: src is "bucket/key", optionally escaped and
// with a leading slash.
func (f *FakeS3) copyObject(w http.ResponseWriter, key, src string) {
	if unescaped, err := url.PathUnescape(src); err == nil {
		src = unescaped
	}
	data, ok := f.Objects["/"+strings.TrimPrefix(src, "/")]
	if !ok {
		writeError(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	f.Objects[key] = slices.Clone(data)
	f.touch(key)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header+`<CopyObjectResult><ETag>"fake"</ETag></CopyObjectResult>`)
}

// touch sets key's last-modified time to now.
func (f *FakeS3) touch(key string) {
	if f.Modified == nil {
		f.Modified = make(map[string]time.Time)
	}
	f.Modified[key] = time.Now()
}

// list answers ListObjectsV2 for the bucket in the path, in key order, a page
// at a time. The continuation token is the last key of the previous page.
func (f *FakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucketPath := "/" + strings.Trim(r.URL.Path, "/") + "/"
	var keys []string
	for path := range f.Objects {
		key, ok := strings.CutPrefix(path, bucketPath)
		if ok && strings.HasPrefix(key, q.Get("prefix")) && key > q.Get("continuation-token") {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	pageSize := 1000
	if f.PageSize > 0 {
		pageSize = f.PageSize
	}
	result := listBucketResult{Prefix: q.Get("prefix"), MaxKeys: pageSize}
	if len(keys) > pageSize {
		keys = keys[:pageSize]
		result.IsTruncated = true
		result.NextContinuationToken = keys[len(keys)-1]
	}
	for _, key := range keys {
		o := listObject{Key: key, Size: len(f.Objects[bucketPath+key])}
		if t, ok := f.Modified[bucketPath+key]; ok {
			o.LastModified = t.UTC().Format(time.RFC3339Nano)
		}
		result.Contents = append(result.Contents, o)
	}
	result.KeyCount = len(keys)

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(result)
}

type listBucketResult struct {
	XMLName               xml.Name     `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Prefix                string       `xml:"Prefix"`
	KeyCount              int          `xml:"KeyCount"`
	MaxKeys               int          `xml:"MaxKeys"`
	IsTruncated           bool         `xml:"IsTruncated"`
	NextContinuationToken string       `xml:"NextContinuationToken,omitempty"`
	Contents              []listObject `xml:"Contents"`
}

type listObject struct {
	Key          string `xml:"Key"`
	Size         int    `xml:"Size"`
	LastModified string `xml:"LastModified,omitempty"`
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
