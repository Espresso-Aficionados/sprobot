package s3client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

// disableURLValidation overrides the URL validator for testing with local servers.
func disableURLValidation(t *testing.T) {
	t.Helper()
	t.Cleanup(AllowLocalFetchesForTesting())
}

// The dial-time check must block loopback even when the URL passed the
// up-front validation, as happens with a redirect or a rebinding DNS name.
func TestFetchBlocksLoopbackAtDial(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := newTestClient(t, server)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	defer target.Close()

	orig := urlValidator
	urlValidator = func(string) error { return nil }
	t.Cleanup(func() { urlValidator = orig })

	if _, err := c.SaveModFile(context.Background(), "123", target.URL+"/secret.png"); err == nil {
		t.Fatal("expected the loopback connection to be blocked")
	}
	if len(fake.objects) != 0 {
		t.Errorf("nothing should be stored, got %d objects", len(fake.objects))
	}
}

// A redirect to a blocked address is caught when its connection is dialed.
func TestFetchBlocksRedirectTarget(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := newTestClient(t, server)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret.png", http.StatusFound)
	}))
	defer redirector.Close()

	disableURLValidation(t)
	blocked := target.Listener.Addr().String()
	dialCheck = func(address string) error {
		if address == blocked {
			return errors.New("blocked")
		}
		return nil
	}

	if _, err := c.SaveModFile(context.Background(), "123", redirector.URL+"/x.png"); err == nil {
		t.Fatal("expected the redirect target to be blocked")
	}
	if len(fake.objects) != 0 {
		t.Errorf("nothing should be stored, got %d objects", len(fake.objects))
	}
}

func TestBlockedIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "::ffff:127.0.0.1": true, "10.1.2.3": true, "192.168.0.1": true,
		"169.254.169.254": true, "fe80::1": true, "0.0.0.0": true, "224.0.0.1": true,
		"100.64.0.1": true, "0.1.2.3": true, "192.0.0.8": true, "198.18.0.1": true, "240.0.0.1": true,
		"fc00::1": true, "64:ff9b::a9fe:a9fe": true, "64:ff9b:1::808:808": true, "2002:7f00:1::": true,
		"::808:808": true, "2001:0:4136:e378::1": true, "fec0::1": true,
		"8.8.8.8": false, "2606:4700::1111": false, "64:ff9b::808:808": false, "2002:808:808::": false,
	} {
		if got := blockedIP(net.ParseIP(addr)); got != want {
			t.Errorf("blockedIP(%s) = %v, want %v", addr, got, want)
		}
	}
}

// fakeS3 is an in-memory S3-compatible server for testing.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string // Content-Type of each PUT
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte), types: make(map[string]string)}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := r.URL.Path

	// Handle ListObjectsV2
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		// The prefix includes the bucket from the path
		bucket := strings.TrimPrefix(key, "/")
		bucket = strings.TrimSuffix(bucket, "/")
		fullPrefix := "/" + bucket + "/" + prefix

		var contents strings.Builder
		for objKey := range f.objects {
			if strings.HasPrefix(objKey, fullPrefix) {
				bareKey := strings.TrimPrefix(objKey, "/"+bucket+"/")
				contents.WriteString(fmt.Sprintf("<Contents><Key>%s</Key></Contents>", bareKey))
			}
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>%s</ListBucketResult>`, contents.String())
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>Not found</Message></Error>`)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.objects[key] = data
		if f.types != nil {
			f.types[key] = r.Header.Get("Content-Type")
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
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

	return &Client{
		s3:          client,
		presigner:   s3.NewPresignClient(client),
		bucket:      bucket,
		endpoint:    endpoint,
		webEndpoint: sprobot.WebEndpoint,
		cache:       cache,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func seedProfile(t *testing.T, fake *fakeS3, bucket, guildID, templateName, userID string, profile map[string]string) {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshaling profile: %v", err)
	}
	key := fmt.Sprintf("/%s/profiles/%s/%s/%s.json", bucket, guildID, templateName, userID)
	fake.mu.Lock()
	fake.objects[key] = data
	fake.mu.Unlock()
}

func TestFetchProfile(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine": "Decent DE1",
		"Grinder": "Niche Zero",
	}
	seedProfile(t, fake, "test-bucket", "123", "Coffee Setup", "456", profile)

	got, err := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if err != nil {
		t.Fatalf("FetchProfile: %v", err)
	}
	if got["Machine"] != "Decent DE1" {
		t.Errorf("Machine = %q, want %q", got["Machine"], "Decent DE1")
	}
	if got["Grinder"] != "Niche Zero" {
		t.Errorf("Grinder = %q, want %q", got["Grinder"], "Niche Zero")
	}
}

func TestFetchProfileNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "999")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFetchProfileCache(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{"Machine": "La Marzocco"}
	seedProfile(t, fake, "test-bucket", "123", "Coffee Setup", "456", profile)

	// First fetch populates cache
	got1, err := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Delete from fake S3 — cache should still serve
	fake.mu.Lock()
	delete(fake.objects, "/test-bucket/profiles/123/Coffee Setup/456.json")
	fake.mu.Unlock()

	got2, err := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got2["Machine"] != got1["Machine"] {
		t.Error("cache did not return same result")
	}
}

func TestFetchProfileCacheReturnsCopy(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{"Machine": "Breville"}
	seedProfile(t, fake, "test-bucket", "123", "Coffee Setup", "456", profile)

	got1, _ := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	got1["Machine"] = "MODIFIED"

	got2, _ := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if got2["Machine"] == "MODIFIED" {
		t.Error("cache returned reference instead of copy — mutations leak")
	}
}

func TestFetchProfileSimple(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{"Roasting Machine": "Aillio Bullet"}
	seedProfile(t, fake, "test-bucket", "123", "Roasting Setup", "456", profile)

	got, err := c.FetchProfileSimple(context.Background(), "123", "Roasting Setup", "456")
	if err != nil {
		t.Fatalf("FetchProfileSimple: %v", err)
	}
	if got["Roasting Machine"] != "Aillio Bullet" {
		t.Errorf("got %q, want %q", got["Roasting Machine"], "Aillio Bullet")
	}
}

func TestFetchProfileSimpleNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.FetchProfileSimple(context.Background(), "123", "Coffee Setup", "999")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSaveProfile(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine": "Gaggia Classic",
		"Grinder": "Eureka Mignon",
	}

	webURL, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr != "" {
		t.Errorf("unexpected userErr: %q", userErr)
	}
	if webURL == "" {
		t.Error("webURL is empty")
	}
	if !strings.Contains(webURL, "bot.espressoaf.com") {
		t.Errorf("webURL %q missing expected domain", webURL)
	}

	// Verify the profile was written to fake S3
	key := "/test-bucket/profiles/123/Coffee Setup/456.json"
	fake.mu.Lock()
	data, ok := fake.objects[key]
	fake.mu.Unlock()
	if !ok {
		t.Fatal("profile not found in fake S3")
	}

	var saved map[string]string
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved profile: %v", err)
	}
	if saved["Machine"] != "Gaggia Classic" {
		t.Errorf("saved Machine = %q, want %q", saved["Machine"], "Gaggia Classic")
	}
}

func TestSaveProfilePopulatesCache(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{"Machine": "Linea Mini"}
	c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)

	// Remove from fake S3 to confirm cache is used
	fake.mu.Lock()
	delete(fake.objects, "/test-bucket/profiles/123/Coffee Setup/456.json")
	fake.mu.Unlock()

	got, err := c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if err != nil {
		t.Fatalf("FetchProfile after save: %v", err)
	}
	if got["Machine"] != "Linea Mini" {
		t.Errorf("cached Machine = %q, want %q", got["Machine"], "Linea Mini")
	}
}

func TestSaveProfileWithImageAlreadyHosted(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	// Image URL already starts with our endpoint — should keep it as-is
	imageURL := server.URL + "/existing-image.png"
	profile := map[string]string{
		"Machine":      "Lelit Bianca",
		"Gear Picture": imageURL,
	}

	_, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr != "" {
		t.Errorf("unexpected userErr: %q", userErr)
	}
}

func TestSaveProfileWithImageDownload(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	// Serve a real PNG from a separate test server
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, color.RGBA{255, 0, 0, 255})
		w.Header().Set("Content-Type", "image/png")
		png.Encode(w, img)
	}))
	defer imgServer.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine":      "Robot",
		"Gear Picture": imgServer.URL + "/photo.png",
	}

	_, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr != "" {
		t.Errorf("unexpected userErr: %q", userErr)
	}

	// The image should have been re-hosted — the profile's Gear Picture should now be a bare S3 key
	key := "/test-bucket/profiles/123/Coffee Setup/456.json"
	fake.mu.Lock()
	data := fake.objects[key]
	fake.mu.Unlock()

	var saved map[string]string
	json.Unmarshal(data, &saved)
	if savedImg := saved["Gear Picture"]; savedImg != "" {
		if !strings.HasPrefix(savedImg, "images/") {
			t.Errorf("re-hosted image key %q doesn't start with images/", savedImg)
		}
	}
}

func TestSaveProfileWithNonImageURL(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	// Serve a text file instead of an image
	textServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "this is not an image")
	}))
	defer textServer.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine":      "Gaggia",
		"Gear Picture": textServer.URL + "/file.txt",
	}

	_, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr == "" {
		t.Error("expected userErr about non-image file, got empty")
	}
	if !strings.Contains(userErr, "can only use images") {
		t.Errorf("userErr %q should mention images", userErr)
	}
}

func TestSaveProfileWithUnreachableURL(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine":      "Pavoni",
		"Gear Picture": "http://127.0.0.1:1/nope.png",
	}

	_, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr == "" {
		t.Error("expected userErr about fetch failure, got empty")
	}
	if !strings.Contains(userErr, "Unable to fetch") {
		t.Errorf("userErr %q should mention fetch failure", userErr)
	}
}

func TestSaveProfileWithBlockedURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{
		"Machine":      "Pavoni",
		"Gear Picture": "http://127.0.0.1:1/nope.png",
	}

	_, userErr, err := c.SaveProfile(context.Background(), sprobot.ProfileTemplate, "123", "456", profile)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if userErr == "" {
		t.Error("expected userErr about blocked URL, got empty")
	}
	if !strings.Contains(userErr, "not valid") {
		t.Errorf("userErr %q should mention URL not valid", userErr)
	}
}

func TestDeleteProfile(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	profile := map[string]string{"Machine": "Rocket"}
	seedProfile(t, fake, "test-bucket", "123", "Coffee Setup", "456", profile)

	// Pre-populate cache
	c.FetchProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")

	err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "123", "456")
	if err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	// Should be gone from S3
	_, err = c.FetchProfileSimple(context.Background(), "123", "Coffee Setup", "456")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Cache should also be evicted
	key := cacheKey("Coffee Setup", "123", "456")
	if _, ok := c.cache.Get(key); ok {
		t.Error("cache should be evicted after delete")
	}
}

func TestSaveModFile(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	// Larger than the old 10 MB cap, which silently truncated.
	payload := bytes.Repeat([]byte("v"), 11<<20)
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)

	got, err := c.SaveModFile(context.Background(), "123", fileServer.URL+"/clip.mp4")
	if err != nil {
		t.Fatalf("SaveModFile: %v", err)
	}
	if _, _, _, ok := sprobot.ParseFileKey(got); !ok || !strings.HasPrefix(got, "mod_files/123/") || !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("key %q is not a valid mod_files/123/*.mp4 key", got)
	}
	stored := "/test-bucket/" + got
	if n := len(fake.objects[stored]); n != len(payload) {
		t.Errorf("stored %d bytes, want %d", n, len(payload))
	}
	if ct := fake.types[stored]; ct != "video/mp4" {
		t.Errorf("stored Content-Type %q, want video/mp4", ct)
	}
}

func TestSaveModFileUnsafeExtension(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)

	got, err := c.SaveModFile(context.Background(), "123", fileServer.URL+"/notes.verylongextension")
	if err != nil {
		t.Fatalf("SaveModFile: %v", err)
	}
	if _, _, name, ok := sprobot.ParseFileKey(got); !ok || strings.Contains(name, ".") {
		t.Errorf("key %q should be valid with the extension dropped", got)
	}
	if ct := fake.types["/test-bucket/"+got]; ct != "application/octet-stream" {
		t.Errorf("stored Content-Type %q, want application/octet-stream", ct)
	}
}

func TestSaveModFileTooLarge(t *testing.T) {
	disableURLValidation(t)
	orig := fileSizeLimit
	fileSizeLimit = 10
	t.Cleanup(func() { fileSizeLimit = orig })

	for name, handler := range map[string]http.HandlerFunc{
		"declared length": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("01234567890"))
		},
		"chunked": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("012345"))
			w.(http.Flusher).Flush()
			w.Write([]byte("67890"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeS3()
			server := httptest.NewServer(fake)
			defer server.Close()
			fileServer := httptest.NewServer(handler)
			defer fileServer.Close()

			c := newTestClient(t, server)

			_, err := c.SaveModFile(context.Background(), "123", fileServer.URL+"/big.mov")
			if !errors.Is(err, ErrFileTooLarge) {
				t.Errorf("err = %v, want ErrFileTooLarge", err)
			}
			if len(fake.objects) != 0 {
				t.Errorf("stored %d objects, want none", len(fake.objects))
			}
		})
	}
}

func TestSaveModFileHTTPError(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusForbidden)
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)

	if _, err := c.SaveModFile(context.Background(), "123", fileServer.URL+"/photo.jpg"); err == nil {
		t.Error("expected error for HTTP 403, got nil")
	}
	if len(fake.objects) != 0 {
		t.Errorf("stored the error page: %d objects", len(fake.objects))
	}
}

func TestSaveModFileUnreachable(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	if _, err := c.SaveModFile(context.Background(), "123", "http://127.0.0.1:1/nope.jpg"); err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
}

func TestSaveModFileBlockedURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	// The default validator blocks loopback.
	if _, err := c.SaveModFile(context.Background(), "123", "http://127.0.0.1:1/nope.jpg"); err == nil {
		t.Error("expected error for blocked URL, got nil")
	}
}

func TestFileURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	const name = "abcdefghijABCDEFGHIJ0123456789"
	want := sprobot.WebEndpoint + "files/mod/123/" + name + ".jpg"

	tests := []struct {
		stored string
		want   string
	}{
		{"mod_files/123/" + name + ".jpg", want},
		{server.URL + "/test-bucket/mod_files/123/" + name + ".jpg", want},
		{server.URL + "/test-bucket/mod_files%2F123%2F" + name + ".jpg", want},
		{server.URL + "/test-bucket/mod_files/123/" + name + ".jpg?X-Amz-Expires=172800&X-Amz-Signature=abc", want},
		{"shortcut_files/123/" + name + ".png", sprobot.WebEndpoint + "files/shortcut/123/" + name + ".png"},
		{"sticky_files/123/" + name, sprobot.WebEndpoint + "files/sticky/123/" + name},
		{"mod_files/123/" + name + "..jpg", sprobot.WebEndpoint + "files/mod/123/" + name + "..jpg"},
		// Unchanged: external URLs, plain text, other prefixes, bad names.
		{"https://cdn.discordapp.com/attachments/1/2/a.png", "https://cdn.discordapp.com/attachments/1/2/a.png"},
		{"just some shortcut text", "just some shortcut text"},
		{"images/123/Coffee Setup/456.png", "images/123/Coffee Setup/456.png"},
		{"mod_files/123/short.jpg", "mod_files/123/short.jpg"},
		{"mod_files/../123/" + name, "mod_files/../123/" + name},
		{"", ""},
	}
	for _, tt := range tests {
		if got := c.FileURL(tt.stored); got != tt.want {
			t.Errorf("FileURL(%q) = %q, want %q", tt.stored, got, tt.want)
		}
	}
}

func TestFileKey(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	const name = "abcdefghijABCDEFGHIJ0123456789"
	key := "sticky_files/123/" + name + ".png"

	tests := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{key, key, true},
		{c.FileURL(key), key, true},
		{server.URL + "/test-bucket/" + key, key, true},
		{server.URL + "/test-bucket/sticky_files%2F123%2F" + name + ".png?X-Amz-Signature=abc", key, true},
		{sprobot.WebEndpoint + "files/mod/123/" + name + ".jpg", "mod_files/123/" + name + ".jpg", true},
		// Not a saved file: external URLs, profile images, bad routes and names.
		{"https://cdn.discordapp.com/attachments/1/2/a.png", "", false},
		{"https://other.example/files/sticky/123/" + name + ".png", "", false},
		{"images/123/Coffee Setup/456.png", "", false},
		{sprobot.WebEndpoint + "files/sticky/123/short.png", "", false},
		{sprobot.WebEndpoint + "files/sticky/../123/" + name, "", false},
		{sprobot.WebEndpoint + "files/other/123/" + name, "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := c.FileKey(tt.ref)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("FileKey(%q) = %q, %v; want %q, %v", tt.ref, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestResolveLegacyLink(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	const name = "abcdefghijABCDEFGHIJ0123456789"
	bucket := server.URL + "/test-bucket/"
	profileImage := sprobot.WebEndpoint + "profiles/1/Coffee%20Setup/2/image.jpg"

	tests := []struct {
		raw    string
		kind   LegacyLinkKind
		newURL string
	}{
		{bucket + "mod_files/1/" + name + "..png", LegacyFile, sprobot.WebEndpoint + "files/mod/1/" + name + "..png"},
		{bucket + "shortcut_files/1/" + name + ".png?X-Amz-Signature=abc", LegacyFile, sprobot.WebEndpoint + "files/shortcut/1/" + name + ".png"},
		{bucket + "images/1/Coffee%20Setup/2.jpg", LegacyProfileImage, profileImage},
		{bucket + "images/1/Coffee%20Setup/2.jpg?X-Amz-Expires=300", LegacyProfileImage, profileImage},
		{server.URL + "/76916743.gif", LegacyAvatar, sprobot.WebEndpoint + sprobot.AvatarWebPath},
		{bucket + "profiles/1/Coffee%20Setup/2.json", LegacyUnknown, ""},
		{server.URL + "/elsewhere/x.png", LegacyUnknown, ""},
		{"https://cdn.discordapp.com/attachments/1/2/a.png", LegacyNone, ""},
		{"https://bot.espressoaf.com/files/mod/1/" + name, LegacyNone, ""},
	}
	for _, tt := range tests {
		got := c.ResolveLegacyLink(tt.raw)
		if got.Kind != tt.kind || got.NewURL != tt.newURL {
			t.Errorf("ResolveLegacyLink(%q) = %v %q, want %v %q", tt.raw, got.Kind, got.NewURL, tt.kind, tt.newURL)
		}
	}

	got := c.ResolveLegacyLink(bucket + "images/1/Coffee%20Setup/2.jpg")
	if got.GuildID != "1" || got.TemplateName != "Coffee Setup" || got.UserID != "2" {
		t.Errorf("profile image parts = %q %q %q", got.GuildID, got.TemplateName, got.UserID)
	}
}

func TestSameProvider(t *testing.T) {
	origin := "https://profile-bot.us-southeast-1.linodeobjects.com"
	for host, want := range map[string]bool{
		"us-southeast-1.linodeobjects.com":  true,
		"other.us-east-1.linodeobjects.com": true,
		"cdn.discordapp.com":                false,
		"linodeobjects.com.evil.example":    false,
		"":                                  false,
	} {
		if got := sameProvider(host, origin); got != want {
			t.Errorf("sameProvider(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestStatFile(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	fake.objects["/test-bucket/mod_files/1/x.pdf"] = []byte("12345")

	if n, err := c.StatFile(context.Background(), "mod_files/1/x.pdf"); err != nil || n != 5 {
		t.Errorf("StatFile = %d, %v; want 5, nil", n, err)
	}
	if _, err := c.StatFile(context.Background(), "mod_files/1/missing.pdf"); !errors.Is(err, ErrNotFound) {
		t.Errorf("StatFile(missing) err = %v, want ErrNotFound", err)
	}
}

func TestPresignFile(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	got, err := c.PresignFile(context.Background(), "mod_files/1/x.pdf", PresignTTLPage, "application/octet-stream", `attachment; filename="x.pdf"`)
	if err != nil {
		t.Fatalf("PresignFile: %v", err)
	}
	for _, want := range []string{"X-Amz-Expires=900", "response-content-type=application%2Foctet-stream", "response-content-disposition=attachment"} {
		if !strings.Contains(got, want) {
			t.Errorf("PresignFile = %q, want it to contain %q", got, want)
		}
	}
}

func TestCacheKey(t *testing.T) {
	k := cacheKey("Coffee Setup", "123", "456")
	if k != "Coffee Setup/123/456" {
		t.Errorf("cacheKey = %q, want %q", k, "Coffee Setup/123/456")
	}
}

func TestRandomString(t *testing.T) {
	s := randomString(30)
	if len(s) != 30 {
		t.Errorf("randomString(30) length = %d, want 30", len(s))
	}
	// Should be alphanumeric
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			t.Errorf("randomString contains non-alphanumeric char: %c", c)
		}
	}
	// Two calls should be different (probabilistically)
	s2 := randomString(30)
	if s == s2 {
		t.Error("two randomString calls returned identical results")
	}
}

func TestFetchStickies(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	// Seed data
	stickyData := []byte(`{"100":{"channel_id":"100","content":"hello"}}`)
	fake.mu.Lock()
	fake.objects["/test-bucket/stickies/123.json"] = stickyData
	fake.mu.Unlock()

	got, err := c.FetchStickies(context.Background(), "123")
	if err != nil {
		t.Fatalf("FetchStickies: %v", err)
	}
	if string(got) != string(stickyData) {
		t.Errorf("got %q, want %q", string(got), string(stickyData))
	}
}

func TestFetchStickiesNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.FetchStickies(context.Background(), "999")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSaveStickies(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	data := []byte(`{"200":{"channel_id":"200","content":"sticky"}}`)
	err := c.SaveStickies(context.Background(), "456", data)
	if err != nil {
		t.Fatalf("SaveStickies: %v", err)
	}

	fake.mu.Lock()
	saved, ok := fake.objects["/test-bucket/stickies/456.json"]
	fake.mu.Unlock()

	if !ok {
		t.Fatal("stickies not found in S3")
	}
	if string(saved) != string(data) {
		t.Errorf("saved %q, want %q", string(saved), string(data))
	}
}

func TestSaveAndFetchStickiesRoundTrip(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	original := []byte(`{"channel":"data"}`)
	if err := c.SaveStickies(context.Background(), "789", original); err != nil {
		t.Fatalf("SaveStickies: %v", err)
	}

	got, err := c.FetchStickies(context.Background(), "789")
	if err != nil {
		t.Fatalf("FetchStickies: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("round trip mismatch: got %q, want %q", string(got), string(original))
	}
}

func TestSaveStickyFile(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-file-data"))
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)

	got, err := c.SaveStickyFile(context.Background(), "123", fileServer.URL+"/attachment.png")
	if err != nil {
		t.Fatalf("SaveStickyFile: %v", err)
	}
	if !strings.HasPrefix(got, "sticky_files/") {
		t.Errorf("result key %q should start with sticky_files/", got)
	}
	if !strings.HasSuffix(got, ".png") {
		t.Errorf("result key %q should end with .png", got)
	}
}

func TestSaveStickyFileBlockedURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	// Loopback URL should be blocked by default validator
	_, err := c.SaveStickyFile(context.Background(), "123", "http://127.0.0.1:1/nope.png")
	if err == nil {
		t.Error("expected error for blocked URL, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("error %q should mention validation", err.Error())
	}
}

func TestSaveStickyFileUnreachable(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.SaveStickyFile(context.Background(), "123", "http://127.0.0.1:1/nope.png")
	if err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
	if !strings.Contains(err.Error(), "fetching file") {
		t.Errorf("error %q should mention fetching", err.Error())
	}
}

func TestNewMissingEnvVars(t *testing.T) {
	tests := []struct {
		name string
		envs map[string]string
		want string
	}{
		{"missing key", map[string]string{}, "S3_KEY"},
		{"missing secret", map[string]string{"S3_KEY": "k"}, "S3_SECRET"},
		{"missing endpoint", map[string]string{"S3_KEY": "k", "S3_SECRET": "s"}, "S3_ENDPOINT"},
		{"missing bucket", map[string]string{"S3_KEY": "k", "S3_SECRET": "s", "S3_ENDPOINT": "e"}, "S3_BUCKET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all
			for _, k := range []string{"S3_KEY", "S3_SECRET", "S3_ENDPOINT", "S3_BUCKET"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.envs {
				t.Setenv(k, v)
			}

			_, err := New()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(fmt.Errorf("random error")) {
		t.Error("should not match random error")
	}
	if !isNotFound(fmt.Errorf("operation: NoSuchKey")) {
		t.Error("should match NoSuchKey in error string")
	}
}

func TestValidateImageURLFileScheme(t *testing.T) {
	err := validateImageURL("file:///etc/passwd")
	if err == nil {
		t.Error("expected error for file:// scheme")
	}
	if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Errorf("error %q should mention unsupported scheme", err.Error())
	}
}

func TestValidateImageURLDataScheme(t *testing.T) {
	err := validateImageURL("data:text/html,<script>alert(1)</script>")
	if err == nil {
		t.Error("expected error for data: scheme")
	}
	if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Errorf("error %q should mention unsupported scheme", err.Error())
	}
}

func TestValidateImageURLLoopback(t *testing.T) {
	err := validateImageURL("http://127.0.0.1/secret")
	if err == nil {
		t.Error("expected error for loopback IP")
	}
	if !strings.Contains(err.Error(), "blocked IP range") {
		t.Errorf("error %q should mention blocked IP", err.Error())
	}
}

func TestValidateImageURLLocalhost(t *testing.T) {
	err := validateImageURL("http://localhost/secret")
	if err == nil {
		t.Error("expected error for localhost")
	}
}

func TestValidateImageURLInvalidURL(t *testing.T) {
	err := validateImageURL("://invalid")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestBucket(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	if c.Bucket() != "test-bucket" {
		t.Errorf("Bucket() = %q, want %q", c.Bucket(), "test-bucket")
	}
}

func TestFetchGuildJSONRoundTrip(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	data := []byte(`{"key":"value"}`)
	if err := c.SaveGuildJSON(context.Background(), "testprefix", "guild1", data); err != nil {
		t.Fatalf("SaveGuildJSON: %v", err)
	}

	got, err := c.FetchGuildJSON(context.Background(), "testprefix", "guild1")
	if err != nil {
		t.Fatalf("FetchGuildJSON: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", string(got), string(data))
	}
}

func TestFetchGuildJSONNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.FetchGuildJSON(context.Background(), "testprefix", "missing")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPresignExistingEmpty(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	got := c.PresignExisting(context.Background(), "", PresignTTLPage)
	if got != "" {
		t.Errorf("PresignExisting('') = %q, want empty", got)
	}
}

func TestPresignExistingExternalURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	ext := "https://cdn.discord.com/attachments/123/456/photo.png"
	got := c.PresignExisting(context.Background(), ext, PresignTTLPage)
	if got != ext {
		t.Errorf("PresignExisting(external) = %q, want %q", got, ext)
	}
}

func TestPresignExistingBareKey(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	got := c.PresignExisting(context.Background(), "images/123/Coffee Setup/456.png", PresignTTLPage)
	if !strings.HasPrefix(got, "http") {
		t.Errorf("PresignExisting(bare key) = %q, should be a full URL", got)
	}
	if !strings.Contains(got, "X-Amz-") {
		t.Errorf("PresignExisting(bare key) = %q, should contain presign params", got)
	}
}

func TestPresignExistingOldFullURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	oldURL := server.URL + "/test-bucket/images%2F123%2FCoffee+Setup%2F456.png"
	got := c.PresignExisting(context.Background(), oldURL, PresignTTLPage)
	if !strings.Contains(got, "X-Amz-") {
		t.Errorf("PresignExisting(old URL) = %q, should contain presign params", got)
	}
}

func TestPresignExistingHonorsTTL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	for ttl, want := range map[time.Duration]string{
		PresignTTLPage: "X-Amz-Expires=900",
		time.Hour:      "X-Amz-Expires=3600",
	} {
		got := c.PresignExisting(context.Background(), "images/1/x.png", ttl)
		if !strings.Contains(got, want) {
			t.Errorf("PresignExisting(ttl=%s) = %q, want it to contain %q", ttl, got, want)
		}
	}
}

func TestProfileImageURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	tmpl := sprobot.ProfileTemplate

	tests := []struct {
		name    string
		profile map[string]string
		want    string
	}{
		{"no image", map[string]string{}, ""},
		{"bare key", map[string]string{"Gear Picture": "images/123/Coffee Setup/456.jpg"},
			sprobot.WebEndpoint + "profiles/123/Coffee%20Setup/456/image.jpg"},
		{"bare key with version", map[string]string{"Gear Picture": "images/123/Coffee Setup/456.png", sprobot.ImageVersionKey: "k3x"},
			sprobot.WebEndpoint + "profiles/123/Coffee%20Setup/456/image.png?v=k3x"},
		{"old full URL", map[string]string{"Gear Picture": server.URL + "/test-bucket/images%2F123%2FCoffee+Setup%2F456.webp"},
			sprobot.WebEndpoint + "profiles/123/Coffee%20Setup/456/image.webp"},
		{"external URL unchanged", map[string]string{"Gear Picture": "https://example.com/a.png"}, "https://example.com/a.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.ProfileImageURL(tmpl, "123", "456", tt.profile); got != tt.want {
				t.Errorf("ProfileImageURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenImage(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	fake.mu.Lock()
	fake.objects["/test-bucket/images/1/x.png"] = []byte("data")
	fake.mu.Unlock()

	body, size, err := c.OpenImage(context.Background(), "images/1/x.png")
	if err != nil {
		t.Fatalf("OpenImage: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "data" || size != 4 {
		t.Errorf("OpenImage = %q (size %d), want \"data\" (size 4)", got, size)
	}

	if _, _, err := c.OpenImage(context.Background(), "images/1/missing.png"); err != ErrNotFound {
		t.Errorf("missing object: err = %v, want ErrNotFound", err)
	}
	if _, _, err := c.OpenImage(context.Background(), "https://example.com/a.png"); err != ErrNotFound {
		t.Errorf("external URL: err = %v, want ErrNotFound", err)
	}
}

func TestSaveProfileImageVersion(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		png.Encode(w, img)
	}))
	defer imgServer.Close()

	c := newTestClient(t, server)
	tmpl := sprobot.ProfileTemplate
	saved := func() map[string]string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		var p map[string]string
		json.Unmarshal(fake.objects["/test-bucket/profiles/123/Coffee Setup/456.json"], &p)
		return p
	}

	// New upload sets a version.
	c.SaveProfile(context.Background(), tmpl, "123", "456", map[string]string{"Gear Picture": imgServer.URL + "/a.png"})
	first := saved()
	v1 := first[sprobot.ImageVersionKey]
	if v1 == "" {
		t.Fatal("re-hosted image should set an image version")
	}

	// Re-saving the already-hosted key with its version keeps it.
	c.SaveProfile(context.Background(), tmpl, "123", "456", map[string]string{
		"Gear Picture": first["Gear Picture"], sprobot.ImageVersionKey: v1, "Machine": "Rocket",
	})
	if got := saved()[sprobot.ImageVersionKey]; got != v1 {
		t.Errorf("unchanged image: version = %q, want %q", got, v1)
	}

	// A failed upload preserves the previous image and its version.
	c.SaveProfile(context.Background(), tmpl, "123", "456", map[string]string{"Gear Picture": "http://[::1]:0/bad"})
	if got := saved(); got["Gear Picture"] != first["Gear Picture"] || got[sprobot.ImageVersionKey] != v1 {
		t.Errorf("failed upload: image = %q version = %q, want previous image and %q", got["Gear Picture"], got[sprobot.ImageVersionKey], v1)
	}

	// Removing the image drops the version.
	c.SaveProfile(context.Background(), tmpl, "123", "456", map[string]string{"Machine": "Rocket", sprobot.ImageVersionKey: v1})
	if got, ok := saved()[sprobot.ImageVersionKey]; ok {
		t.Errorf("no image: version = %q, want absent", got)
	}
}

// A re-upload stored under a new extension deletes the upload it replaces; one
// under the same key overwrites it in place.
func TestSaveProfileDeletesReplacedImage(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	}))
	defer imgServer.Close()

	c := newTestClient(t, server)
	tmpl := sprobot.ProfileTemplate
	const oldKey, newKey = "images/123/Coffee Setup/456.jpg", "images/123/Coffee Setup/456.png"
	const otherUser = "/test-bucket/images/123/Coffee Setup/789.jpg"
	seedProfile(t, fake, "test-bucket", "123", tmpl.Name, "456", map[string]string{"Machine": "Rocket", "Gear Picture": oldKey})
	fake.mu.Lock()
	fake.objects["/test-bucket/"+oldKey] = []byte("old jpeg")
	fake.objects[otherUser] = []byte("someone else's upload")
	fake.mu.Unlock()
	stored := func(key string) bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.objects[key]
		return ok
	}
	upload := func() {
		t.Helper()
		_, userErr, err := c.SaveProfile(context.Background(), tmpl, "123", "456", map[string]string{"Machine": "Rocket", "Gear Picture": imgServer.URL + "/a.png"})
		if err != nil || userErr != "" {
			t.Fatalf("SaveProfile = %q, %v", userErr, err)
		}
	}

	upload()
	if stored("/test-bucket/" + oldKey) {
		t.Error("the replaced .jpg upload should be deleted")
	}
	if !stored("/test-bucket/"+newKey) || !stored(otherUser) {
		t.Error("the new upload and other users' images must be kept")
	}

	// Same extension again: the key is reused, so nothing may be deleted.
	upload()
	if !stored("/test-bucket/" + newKey) {
		t.Error("re-uploading under the same key must not delete it")
	}
}

func TestProfileImageKey(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)
	tests := []struct{ value, want string }{
		{"images/1/Coffee Setup/2.png", "images/1/Coffee Setup/2.png"},
		{server.URL + "/test-bucket/images%2F1%2FCoffee%20Setup%2F2.jpeg", "images/1/Coffee Setup/2.jpeg"},
		{server.URL + "/test-bucket/images/1/Coffee%20Setup/2.jpg?X-Amz-Expires=300", "images/1/Coffee Setup/2.jpg"},
		// Not this user's own upload.
		{"images/1/Coffee Setup/3.png", ""},
		{"images/1/Roasting Setup/2.png", ""},
		{"images/9/Coffee Setup/2.png", ""},
		{"stickies/1.json", ""},
		{"mod_files/1/abcdefghijABCDEFGHIJ0123456789.png", ""},
		{"https://cdn.discordapp.com/attachments/1/2/a.png", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, ok := c.ProfileImageKey(sprobot.ProfileTemplate, "1", "2", map[string]string{"Gear Picture": tt.value})
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("ProfileImageKey(%q) = %q, %v; want %q", tt.value, got, ok, tt.want)
		}
	}
}

// Every writer builds its key with sprobot.FileKey, so it never stores a key
// the web viewer would refuse to serve.
func TestSaveFileKeysMatchViewer(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)
	ctx := context.Background()
	src := fileServer.URL + "/a.png"
	for kind, save := range map[string]func(guildID string) (string, error){
		sprobot.FileKindMod:      func(g string) (string, error) { return c.SaveModFile(ctx, g, src) },
		sprobot.FileKindShortcut: func(g string) (string, error) { return c.SaveShortcutImage(ctx, g, src) },
		sprobot.FileKindSticky:   func(g string) (string, error) { return c.SaveStickyFile(ctx, g, src) },
	} {
		key, err := save("123")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if gotKind, guildID, _, ok := sprobot.ParseFileKey(key); !ok || gotKind != kind || guildID != "123" {
			t.Errorf("%s: key %q parses as kind %q, guild %q (ok=%v)", kind, key, gotKind, guildID, ok)
		}
		if key, err := save("not-a-guild"); err == nil {
			t.Errorf("%s: saved %q for an invalid guild ID, want an error", kind, key)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.objects) != 3 {
		t.Errorf("stored %d objects, want 3 (nothing for the invalid guild)", len(fake.objects))
	}
}
