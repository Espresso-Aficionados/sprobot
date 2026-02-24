package s3client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

const maxImageSize = 10 * 1024 * 1024 // 10 MB

var httpClient = &http.Client{Timeout: 30 * time.Second}

// urlValidator is the function used to validate URLs before fetching.
// It is a variable so tests can override it.
var urlValidator = validateImageURL

// validateImageURL checks that the URL uses an allowed scheme and does not
// resolve to a private/loopback IP range.
func validateImageURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("DNS lookup failed for %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("URL resolves to a blocked IP range")
		}
	}
	return nil
}

var ErrNotFound = errors.New("profile not found")

func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	return errors.As(err, &nsk) || strings.Contains(err.Error(), "NoSuchKey")
}

type Client struct {
	s3       *s3.Client
	bucket   string
	endpoint string
	cache    *lru.Cache[string, map[string]string]
	log      *slog.Logger
}

func New() (*Client, error) {
	key := os.Getenv("S3_KEY")
	if key == "" {
		return nil, fmt.Errorf("S3_KEY env var is undefined")
	}
	secret := os.Getenv("S3_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("S3_SECRET env var is undefined")
	}
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("S3_ENDPOINT env var is undefined")
	}
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET env var is undefined")
	}

	cache, err := lru.New[string, map[string]string](500)
	if err != nil {
		return nil, fmt.Errorf("creating LRU cache: %w", err)
	}

	client := s3.New(s3.Options{
		Region:           "us-southeast-1",
		BaseEndpoint:     &endpoint,
		Credentials:      credentials.NewStaticCredentialsProvider(key, secret, ""),
		UsePathStyle:     true,
		RetryMaxAttempts: 5,
	})

	return &Client{
		s3:       client,
		bucket:   bucket,
		endpoint: endpoint,
		cache:    cache,
		log:      slog.Default(),
	}, nil
}

// NewDirect creates a Client with explicitly provided dependencies.
func NewDirect(s3Client *s3.Client, bucket, endpoint string, cache *lru.Cache[string, map[string]string], log *slog.Logger) *Client {
	return &Client{
		s3:       s3Client,
		bucket:   bucket,
		endpoint: endpoint,
		cache:    cache,
		log:      log,
	}
}

// Bucket returns the configured S3 bucket name.
func (c *Client) Bucket() string {
	return c.bucket
}

func cacheKey(templateName, guildID, userID string) string {
	return templateName + "/" + guildID + "/" + userID
}

func (c *Client) FetchProfile(ctx context.Context, tmpl sprobot.Template, guildID, userID string) (map[string]string, error) {
	c.log.Info("Fetching profile", "user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	key := cacheKey(tmpl.Name, guildID, userID)
	if profile, ok := c.cache.Get(key); ok {
		c.log.Info("Cache hit", "user_id", userID, "template", tmpl.Name, "guild_id", guildID)
		cp := make(map[string]string, len(profile))
		for k, v := range profile {
			cp[k] = v
		}
		return cp, nil
	}
	c.log.Info("Cache miss", "user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	s3Path := sprobot.ProfileS3Path(guildID, tmpl.Name, userID)
	start := time.Now()
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetching profile from s3: %w", err)
	}
	defer out.Body.Close()

	c.log.Info(fmt.Sprintf("s3 fetch time: %dms", time.Since(start).Milliseconds()),
		"user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	var profile map[string]string
	if err := json.NewDecoder(out.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decoding profile json: %w", err)
	}

	c.cache.Add(key, profile)

	cp := make(map[string]string, len(profile))
	for k, v := range profile {
		cp[k] = v
	}
	return cp, nil
}

func (c *Client) FetchProfileSimple(ctx context.Context, guildID, templateName, userID string) (map[string]string, error) {
	s3Path := sprobot.ProfileS3Path(guildID, templateName, userID)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetching profile from s3: %w", err)
	}
	defer out.Body.Close()

	var profile map[string]string
	if err := json.NewDecoder(out.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decoding profile json: %w", err)
	}
	return profile, nil
}

func (c *Client) SaveProfile(ctx context.Context, tmpl sprobot.Template, guildID, userID string, profile map[string]string) (webURL string, userErr string, err error) {
	c.log.Info("Saving profile", "user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	// Step 1: re-host the image if needed
	userErr, imageURL := c.getImageS3URL(ctx, tmpl, guildID, userID, profile)
	if imageURL != "" {
		profile[tmpl.Image.Name] = imageURL
	} else if userErr != "" {
		// Validation failed — preserve the previously saved image instead of
		// discarding it.
		existing, fetchErr := c.FetchProfile(ctx, tmpl, guildID, userID)
		if fetchErr == nil {
			if prev, ok := existing[tmpl.Image.Name]; ok && prev != "" {
				profile[tmpl.Image.Name] = prev
			} else {
				delete(profile, tmpl.Image.Name)
			}
		} else {
			delete(profile, tmpl.Image.Name)
		}
	} else {
		delete(profile, tmpl.Image.Name)
	}

	// Step 2: save the JSON profile
	s3Path := sprobot.ProfileS3Path(guildID, tmpl.Name, userID)
	body, err := json.Marshal(profile)
	if err != nil {
		return "", "", fmt.Errorf("marshaling profile: %w", err)
	}

	start := time.Now()
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return "", "", fmt.Errorf("saving profile to s3: %w", err)
	}
	c.log.Info(fmt.Sprintf("s3 write time: %dms", time.Since(start).Milliseconds()),
		"user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	key := cacheKey(tmpl.Name, guildID, userID)
	c.cache.Add(key, profile)

	webURL = sprobot.WebEndpoint + url.PathEscape(c.bucket+"/"+s3Path)
	c.log.Info("Profile saved", "user_id", userID, "template", tmpl.Name, "guild_id", guildID, "profile_url", webURL)

	return webURL, userErr, nil
}

func (c *Client) DeleteProfile(ctx context.Context, tmpl sprobot.Template, guildID, userID string) error {
	c.log.Info("Deleting profile", "user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	key := cacheKey(tmpl.Name, guildID, userID)
	c.cache.Remove(key)

	s3Path := sprobot.ProfileS3Path(guildID, tmpl.Name, userID)
	start := time.Now()
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
	})
	if err != nil {
		return fmt.Errorf("deleting profile from s3: %w", err)
	}

	c.log.Info(fmt.Sprintf("s3 delete time: %dms", time.Since(start).Milliseconds()),
		"user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	return nil
}

func (c *Client) SaveModImage(ctx context.Context, guildID string, fileURL string) (string, error) {
	c.log.Info("Saving file to mod log", "guild_id", guildID)

	if err := urlValidator(fileURL); err != nil {
		c.log.Info("URL validation failed", "url", fileURL, "error", err)
		return fileURL, nil
	}

	resp, err := httpClient.Get(fileURL)
	if err != nil {
		c.log.Info("Unable to fetch from the link provided", "url", fileURL, "error", err)
		return fileURL, nil
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		c.log.Info("Unable to read response body", "url", fileURL, "error", err)
		return fileURL, nil
	}

	randomID := randomString(30)
	parsed, err := url.Parse(fileURL)
	if err != nil {
		c.log.Info("Unable to parse URL for extension", "url", fileURL, "error", err)
		return fileURL, nil
	}
	ext := path.Ext(parsed.Path)
	s3Path := fmt.Sprintf("mod_files/%s/%s%s", guildID, randomID, ext)

	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(data),
		ACL:    s3types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		c.log.Info("Unable to upload to s3", "error", err)
		return fileURL, nil
	}

	s3FinalURL := c.buildS3URL(s3Path)
	c.log.Info("Mod image saved", "guild_id", guildID, "s3_url", s3FinalURL)
	return s3FinalURL, nil
}

func (c *Client) SaveShortcutImage(ctx context.Context, guildID string, fileURL string) (string, error) {
	c.log.Info("Saving shortcut image", "guild_id", guildID)

	if strings.HasPrefix(fileURL, c.endpoint) {
		return fileURL, nil
	}

	if err := urlValidator(fileURL); err != nil {
		c.log.Info("URL validation failed", "url", fileURL, "error", err)
		return fileURL, nil
	}

	resp, err := httpClient.Get(fileURL)
	if err != nil {
		c.log.Info("Unable to fetch from the link provided", "url", fileURL, "error", err)
		return fileURL, nil
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		c.log.Info("Unable to read response body", "url", fileURL, "error", err)
		return fileURL, nil
	}

	randomID := randomString(30)
	parsed, err := url.Parse(fileURL)
	if err != nil {
		c.log.Info("Unable to parse URL for extension", "url", fileURL, "error", err)
		return fileURL, nil
	}
	ext := path.Ext(parsed.Path)
	s3Path := fmt.Sprintf("shortcut_files/%s/%s%s", guildID, randomID, ext)

	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(data),
		ACL:    s3types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		c.log.Info("Unable to upload to s3", "error", err)
		return fileURL, nil
	}

	s3FinalURL := c.buildS3URL(s3Path)
	c.log.Info("Shortcut image saved", "guild_id", guildID, "s3_url", s3FinalURL)
	return s3FinalURL, nil
}

func (c *Client) getImageS3URL(ctx context.Context, tmpl sprobot.Template, guildID, userID string, profile map[string]string) (userErr string, s3URL string) {
	maybeURL, ok := profile[tmpl.Image.Name]
	if !ok || maybeURL == "" {
		return "", ""
	}

	if strings.HasPrefix(maybeURL, c.endpoint) {
		return "", maybeURL
	}

	if err := urlValidator(maybeURL); err != nil {
		return "The URL provided is not valid. Make sure it's a publicly accessible image URL and try again. The rest of your profile has been saved.", ""
	}

	resp, err := httpClient.Get(maybeURL)
	if err != nil {
		return "Unable to fetch from the URL provided, make sure it's an image and try again. The rest of your profile has been saved.", ""
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		return "Unable to fetch from the URL provided, make sure it's an image and try again. The rest of your profile has been saved.", ""
	}

	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return fmt.Sprintf("It looks like you uploaded a %s, but we can only use images. The rest of your profile has been saved. If this looked like a gif, discord probably used a mp4.", ct), ""
	}

	ext := "bin"
	switch ct {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	case "image/gif":
		ext = "gif"
	case "image/webp":
		ext = "webp"
	case "image/bmp":
		ext = "bmp"
	}

	s3Path := fmt.Sprintf("images/%s/%s/%s.%s", guildID, tmpl.Name, userID, ext)

	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(data),
		ACL:    s3types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return "Unable to save image. The rest of your profile has been saved.", ""
	}

	s3FinalURL := c.buildS3URL(s3Path)
	c.log.Info("Profile image saved", "user_id", userID, "template", tmpl.Name, "guild_id", guildID, "s3_url", s3FinalURL)
	return "", s3FinalURL
}

func (c *Client) buildS3URL(s3Path string) string {
	base := strings.TrimRight(c.endpoint, "/")
	return base + "/" + c.bucket + "/" + url.PathEscape(s3Path)
}

func (c *Client) FetchTopPosters(ctx context.Context, guildID string) (map[string]map[string]int, error) {
	s3Path := fmt.Sprintf("topposters/%s.json", guildID)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetching top posters from s3: %w", err)
	}
	defer out.Body.Close()

	var data map[string]map[string]int
	if err := json.NewDecoder(out.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding top posters json: %w", err)
	}
	return data, nil
}

func (c *Client) SaveTopPosters(ctx context.Context, guildID string, data map[string]map[string]int) error {
	s3Path := fmt.Sprintf("topposters/%s.json", guildID)
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling top posters: %w", err)
	}

	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return fmt.Errorf("saving top posters to s3: %w", err)
	}
	return nil
}

const maxGuildJSONSize = 100 * 1024 * 1024 // 100 MB

// FetchGuildJSON fetches {prefix}/{guildID}.json from S3.
func (c *Client) FetchGuildJSON(ctx context.Context, prefix, guildID string) ([]byte, error) {
	s3Path := fmt.Sprintf("%s/%s.json", prefix, guildID)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetching %s from s3: %w", prefix, err)
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, maxGuildJSONSize))
}

// SaveGuildJSON saves data to {prefix}/{guildID}.json in S3.
func (c *Client) SaveGuildJSON(ctx context.Context, prefix, guildID string, data []byte) error {
	s3Path := fmt.Sprintf("%s/%s.json", prefix, guildID)
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("saving %s to s3: %w", prefix, err)
	}
	return nil
}

func (c *Client) FetchStickies(ctx context.Context, guildID string) ([]byte, error) {
	return c.FetchGuildJSON(ctx, "stickies", guildID)
}

func (c *Client) SaveStickies(ctx context.Context, guildID string, data []byte) error {
	return c.SaveGuildJSON(ctx, "stickies", guildID, data)
}

func (c *Client) FetchThreadReminders(ctx context.Context, guildID string) ([]byte, error) {
	return c.FetchGuildJSON(ctx, "threadreminders", guildID)
}

func (c *Client) SaveThreadReminders(ctx context.Context, guildID string, data []byte) error {
	return c.SaveGuildJSON(ctx, "threadreminders", guildID, data)
}

func (c *Client) FetchThreadMemberCounts(ctx context.Context, guildID string) ([]byte, error) {
	return c.FetchGuildJSON(ctx, "threadmembercounts", guildID)
}

func (c *Client) SaveThreadMemberCounts(ctx context.Context, guildID string, data []byte) error {
	return c.SaveGuildJSON(ctx, "threadmembercounts", guildID, data)
}

func (c *Client) SaveStickyFile(ctx context.Context, guildID, fileURL string) (string, error) {
	if err := urlValidator(fileURL); err != nil {
		return "", fmt.Errorf("URL validation failed: %w", err)
	}

	resp, err := httpClient.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("fetching file: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	randomID := randomString(30)
	parsed, err := url.Parse(fileURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	ext := path.Ext(parsed.Path)
	s3Path := fmt.Sprintf("sticky_files/%s/%s%s", guildID, randomID, ext)

	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(data),
		ACL:    s3types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return "", fmt.Errorf("uploading to s3: %w", err)
	}

	return c.buildS3URL(s3Path), nil
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}
