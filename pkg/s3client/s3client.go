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

var ErrNotFound = errors.New("profile not found")

type Client struct {
	s3       *s3.Client
	bucket   string
	endpoint string
	cache    *lru.Cache[string, map[string]string]
	log      *slog.Logger
}

func New() (*Client, error) {
	key := os.Getenv("SPROBOT_S3_KEY")
	if key == "" {
		return nil, fmt.Errorf("SPROBOT_S3_KEY env var is undefined")
	}
	secret := os.Getenv("SPROBOT_S3_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("SPROBOT_S3_SECRET env var is undefined")
	}
	endpoint := os.Getenv("SPROBOT_S3_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("SPROBOT_S3_ENDPOINT env var is undefined")
	}
	bucket := os.Getenv("SPROBOT_S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("SPROBOT_S3_BUCKET env var is undefined")
	}

	cache, err := lru.New[string, map[string]string](500)
	if err != nil {
		return nil, fmt.Errorf("creating LRU cache: %w", err)
	}

	client := s3.New(s3.Options{
		Region:       "us-southeast-1",
		BaseEndpoint: &endpoint,
		Credentials:  credentials.NewStaticCredentialsProvider(key, secret, ""),
		UsePathStyle: true,
	})

	return &Client{
		s3:       client,
		bucket:   bucket,
		endpoint: endpoint,
		cache:    cache,
		log:      slog.Default(),
	}, nil
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
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		// Also check for the generic "NoSuchKey" error code string
		if strings.Contains(err.Error(), "NoSuchKey") {
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
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		if strings.Contains(err.Error(), "NoSuchKey") {
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

	resp, err := http.Get(fileURL)
	if err != nil {
		c.log.Info("Unable to fetch from the link provided", "url", fileURL, "error", err)
		return fileURL, nil
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Info("Unable to read response body", "url", fileURL, "error", err)
		return fileURL, nil
	}

	randomID := randomString(30)
	parsed, _ := url.Parse(fileURL)
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

func (c *Client) getImageS3URL(ctx context.Context, tmpl sprobot.Template, guildID, userID string, profile map[string]string) (userErr string, s3URL string) {
	maybeURL, ok := profile[tmpl.Image.Name]
	if !ok || maybeURL == "" {
		return "", ""
	}

	if strings.HasPrefix(maybeURL, c.endpoint) {
		return "", maybeURL
	}

	resp, err := http.Get(maybeURL)
	if err != nil {
		return "Unable to fetch from the URL provided, make sure it's an image and try again. The rest of your profile has been saved.", ""
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
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

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}
