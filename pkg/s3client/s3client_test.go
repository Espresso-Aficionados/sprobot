package s3client

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

// disableURLValidation overrides the URL validator for testing with local servers.
func disableURLValidation(t *testing.T) {
	t.Helper()
	orig := urlValidator
	urlValidator = func(string) error { return nil }
	t.Cleanup(func() { urlValidator = orig })
}

// fakeS3 is an in-memory S3-compatible server for testing.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := r.URL.Path

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

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.objects[key] = data
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
		s3:       client,
		bucket:   bucket,
		endpoint: endpoint,
		cache:    cache,
		log:      discardLogger(),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

	// The image should have been re-hosted — the profile's Gear Picture should now point at our endpoint
	key := "/test-bucket/profiles/123/Coffee Setup/456.json"
	fake.mu.Lock()
	data := fake.objects[key]
	fake.mu.Unlock()

	var saved map[string]string
	json.Unmarshal(data, &saved)
	if savedImg := saved["Gear Picture"]; savedImg != "" {
		if !strings.HasPrefix(savedImg, server.URL) {
			t.Errorf("re-hosted image URL %q doesn't start with endpoint %q", savedImg, server.URL)
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

func TestSaveModImage(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	// Serve a file to download
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-image-data"))
	}))
	defer fileServer.Close()

	c := newTestClient(t, server)

	got, err := c.SaveModImage(context.Background(), "123", fileServer.URL+"/photo.jpg")
	if err != nil {
		t.Fatalf("SaveModImage: %v", err)
	}
	if !strings.HasPrefix(got, server.URL) {
		t.Errorf("result URL %q should start with endpoint %q", got, server.URL)
	}
	if !strings.Contains(got, "mod_files") {
		t.Errorf("result URL %q should contain mod_files", got)
	}
}

func TestSaveModImageUnreachable(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	original := "http://127.0.0.1:1/nope.jpg"
	got, err := c.SaveModImage(context.Background(), "123", original)
	if err != nil {
		t.Fatalf("SaveModImage: %v", err)
	}
	// Should gracefully return the original URL
	if got != original {
		t.Errorf("expected original URL %q on failure, got %q", original, got)
	}
}

func TestSaveModImageBlockedURL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	original := "http://127.0.0.1:1/nope.jpg"
	got, err := c.SaveModImage(context.Background(), "123", original)
	if err != nil {
		t.Fatalf("SaveModImage: %v", err)
	}
	// URL validation blocks loopback — should return original URL
	if got != original {
		t.Errorf("expected original URL %q on blocked, got %q", original, got)
	}
}

func TestCacheKey(t *testing.T) {
	k := cacheKey("Coffee Setup", "123", "456")
	if k != "Coffee Setup/123/456" {
		t.Errorf("cacheKey = %q, want %q", k, "Coffee Setup/123/456")
	}
}

func TestBuildS3URL(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestClient(t, server)

	got := c.buildS3URL("images/123/Coffee Setup/456.png")
	if !strings.HasPrefix(got, server.URL) {
		t.Errorf("buildS3URL result %q should start with endpoint", got)
	}
	if !strings.Contains(got, "test-bucket") {
		t.Errorf("buildS3URL result %q should contain bucket name", got)
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

func TestNewMissingEnvVars(t *testing.T) {
	tests := []struct {
		name string
		envs map[string]string
		want string
	}{
		{"missing key", map[string]string{}, "SPROBOT_S3_KEY"},
		{"missing secret", map[string]string{"SPROBOT_S3_KEY": "k"}, "SPROBOT_S3_SECRET"},
		{"missing endpoint", map[string]string{"SPROBOT_S3_KEY": "k", "SPROBOT_S3_SECRET": "s"}, "SPROBOT_S3_ENDPOINT"},
		{"missing bucket", map[string]string{"SPROBOT_S3_KEY": "k", "SPROBOT_S3_SECRET": "s", "SPROBOT_S3_ENDPOINT": "e"}, "SPROBOT_S3_BUCKET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all
			for _, k := range []string{"SPROBOT_S3_KEY", "SPROBOT_S3_SECRET", "SPROBOT_S3_ENDPOINT", "SPROBOT_S3_BUCKET"} {
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
