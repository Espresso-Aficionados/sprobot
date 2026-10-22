package s3client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

const maxImageSize = 10 * 1024 * 1024 // 10 MB

// MaxFileSize is the largest attachment Discord accepts from anyone (full
// Nitro), so any file posted in Discord can be saved whole.
const MaxFileSize = 500 << 20

// ErrFileTooLarge is returned when a file exceeds MaxFileSize.
var ErrFileTooLarge = errors.New("file too large")

// fileSizeLimit is MaxFileSize, as a variable so tests can lower it.
var fileSizeLimit int64 = MaxFileSize

var httpClient = newFetchClient(30*time.Second, 0)

// downloadClient fetches files to save. Saves of large files can take minutes,
// so there is no overall timeout; callers bound them with their context.
var downloadClient = newFetchClient(0, 30*time.Second)

// newFetchClient returns a client for fetching user-supplied URLs. Every
// connection, including each redirect hop, is checked against blockedIP at
// dial time, so redirects and DNS rebinding can't reach internal addresses.
// Proxies are disabled so the checked address is the one connected to.
func newFetchClient(timeout, responseHeaderTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error { return dialCheck(address) },
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = dialer.DialContext
	t.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Timeout: timeout, Transport: t}
}

// dialCheck rejects connections to blocked addresses. It is a variable so
// tests can reach local servers.
var dialCheck = checkDialAddress

func checkDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || blockedIP(ip) {
		return fmt.Errorf("connection to %s blocked", host)
	}
	return nil
}

// blockedPrefixes are non-public ranges the net.IP predicates don't cover.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),          // IPv4-compatible (deprecated)
	netip.MustParsePrefix("64:ff9b:1::/48"), // local NAT64; embedding depends on the operator
	netip.MustParsePrefix("2001::/32"),      // Teredo
	netip.MustParsePrefix("fec0::/10"),      // site-local (deprecated)
}

// blockedIP reports whether ip is anything but a public unicast address.
// IPv4 addresses embedded in NAT64 and 6to4 addresses are checked too.
func blockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if v4, ok := embeddedIPv4(addr); ok {
		addr = v4
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

var (
	nat64Prefix     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourPrefix = netip.MustParsePrefix("2002::/16")
)

// embeddedIPv4 returns the IPv4 address carried in a well-known NAT64 or a
// 6to4 address.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	switch {
	case !addr.Is6():
		return netip.Addr{}, false
	case nat64Prefix.Contains(addr):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFourPrefix.Contains(addr):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// urlValidator is the function used to validate URLs before fetching.
// It is a variable so tests can override it.
var urlValidator = validateImageURL

// validateImageURL checks that the URL uses an allowed scheme and does not
// resolve to a blocked IP range. It gives an early, clear error; the fetch
// clients enforce the IP check on the address actually dialed.
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
		if ip := net.ParseIP(ipStr); ip != nil && blockedIP(ip) {
			return fmt.Errorf("URL resolves to a blocked IP range")
		}
	}
	return nil
}

var ErrNotFound = errors.New("profile not found")

func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound // HeadObject's error, which has no body to carry NoSuchKey
	return errors.As(err, &nsk) || errors.As(err, &nf) || strings.Contains(err.Error(), "NoSuchKey")
}

type Client struct {
	s3          *s3.Client
	presigner   *s3.PresignClient
	bucket      string
	endpoint    string
	webEndpoint string
	cache       *lru.Cache[string, map[string]string]
	log         *slog.Logger
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
		s3:          client,
		presigner:   s3.NewPresignClient(client),
		bucket:      bucket,
		endpoint:    endpoint,
		webEndpoint: sprobot.WebEndpointFromEnv(),
		cache:       cache,
		log:         slog.Default(),
	}, nil
}

// NewDirect creates a Client with explicitly provided dependencies.
func NewDirect(s3Client *s3.Client, bucket, endpoint string, cache *lru.Cache[string, map[string]string], log *slog.Logger) *Client {
	return &Client{
		s3:          s3Client,
		presigner:   s3.NewPresignClient(s3Client),
		bucket:      bucket,
		endpoint:    endpoint,
		webEndpoint: sprobot.WebEndpoint,
		cache:       cache,
		log:         log,
	}
}

// Origin returns the scheme and host that presigned URLs point at.
func (c *Client) Origin() string {
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Bucket returns the configured S3 bucket name.
func (c *Client) Bucket() string {
	return c.bucket
}

// PresignTTLPage is the lifetime of presigned URLs, which are only ever
// rendered into a page that re-signs them on every load, so they only need to
// outlive the page's own fetch.
const PresignTTLPage = 15 * time.Minute

// PresignedURL returns a presigned GET URL for the given S3 key that expires after ttl.
func (c *Client) PresignedURL(ctx context.Context, s3Key string, ttl time.Duration) (string, error) {
	out, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Key,
	}, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("presigning %q: %w", s3Key, err)
	}
	return out.URL, nil
}

// PresignExisting generates a presigned URL for a value that may be a bare S3
// key, a full S3 URL from the old public-read scheme, or an external URL.
//   - Empty string → returns ""
//   - Starts with "{endpoint}/{bucket}/" → extracts key, presigns
//   - Does NOT start with "http" → treated as bare S3 key, presigns
//   - Otherwise (external URL) → returned unchanged
//
// Best-effort: logs errors and returns the input on failure.
func (c *Client) PresignExisting(ctx context.Context, urlOrKey string, ttl time.Duration) string {
	key, ok := c.objectKey(urlOrKey)
	if !ok {
		return urlOrKey
	}
	presigned, err := c.PresignedURL(ctx, key, ttl)
	if err != nil {
		c.log.Error("PresignExisting: presign failed", "key", key, "error", err)
		return urlOrKey
	}
	return presigned
}

// objectKey resolves a stored image value to an S3 key in our bucket. It
// returns false for empty values, external URLs, and unparseable old-style URLs.
func (c *Client) objectKey(urlOrKey string) (string, bool) {
	if urlOrKey == "" {
		return "", false
	}

	// Old-style full S3 URL: extract the key portion.
	prefix := strings.TrimRight(c.endpoint, "/") + "/" + c.bucket + "/"
	if strings.HasPrefix(urlOrKey, prefix) {
		raw := strings.TrimPrefix(urlOrKey, prefix)
		key, err := url.PathUnescape(raw)
		if err != nil {
			c.log.Error("objectKey: failed to unescape key", "raw", raw, "error", err)
			return "", false
		}
		return key, true
	}

	// Bare S3 key (not an HTTP URL).
	if !strings.HasPrefix(urlOrKey, "http") {
		return urlOrKey, true
	}

	// External URL.
	return "", false
}

// ProfileImageURL returns a non-expiring URL for a profile's image, suitable
// for Discord embeds: images in our bucket go through the web server's
// Discord-only image route; external URLs are returned unchanged. Returns ""
// when the profile has no image.
func (c *Client) ProfileImageURL(tmpl sprobot.Template, guildID, userID string, profile map[string]string) string {
	value := profile[tmpl.Image.Name]
	key, ok := c.objectKey(value)
	if !ok {
		return value
	}
	u := c.webEndpoint + sprobot.ProfileImageWebPath(guildID, tmpl.Name, userID, path.Ext(key))
	if v := profile[sprobot.ImageVersionKey]; v != "" {
		u += "?v=" + url.QueryEscape(v)
	}
	return u
}

// FileURL maps a stored reference to a saved file (a bare key or an old bucket
// URL, presigned or not) to its permanent web route. Anything else, such as
// external URLs or plain text, is returned unchanged.
func (c *Client) FileURL(stored string) string {
	key, ok := c.objectKey(strings.TrimSpace(stored))
	if !ok {
		return stored
	}
	if i := strings.IndexByte(key, '?'); i >= 0 {
		key = key[:i]
	}
	kind, guildID, name, ok := sprobot.ParseFileKey(key)
	if !ok {
		return stored
	}
	return c.webEndpoint + sprobot.FileWebPath(kind, guildID, name)
}

// LegacyLinkKind classifies a URL found in a message posted while files were
// linked straight from the bucket.
type LegacyLinkKind int

const (
	LegacyNone         LegacyLinkKind = iota // not a link into our bucket
	LegacyFile                               // a saved mod, shortcut, or sticky file
	LegacyProfileImage                       // a profile image
	LegacyAvatar                             // the bot avatar, once hosted in the bucket
	LegacyUnknown                            // our bucket's host, but no stable route for it
)

// legacyAvatarKey is where the bot avatar lived before the web server hosted it.
const legacyAvatarKey = "76916743.gif"

// LegacyLink is a bucket URL resolved to the web route that replaces it.
type LegacyLink struct {
	Kind    LegacyLinkKind
	NewURL  string
	GuildID string // set for LegacyFile and LegacyProfileImage
	// Set for LegacyProfileImage.
	TemplateName, UserID string
}

// ResolveLegacyLink maps a direct or presigned bucket URL to its stable web
// route. URLs on the bucket's host that it can't map are LegacyUnknown, so
// callers can report them.
func (c *Client) ResolveLegacyLink(raw string) LegacyLink {
	origin := c.Origin()
	if origin == "" || !strings.HasPrefix(raw, origin+"/") {
		// Same storage provider under another hostname: possibly an old URL
		// form, so surface it rather than silently skipping it.
		if u, err := url.Parse(raw); err == nil && sameProvider(u.Hostname(), origin) {
			return LegacyLink{Kind: LegacyUnknown}
		}
		return LegacyLink{}
	}
	if u, err := url.Parse(raw); err == nil && u.Path == "/"+legacyAvatarKey {
		return LegacyLink{Kind: LegacyAvatar, NewURL: c.webEndpoint + sprobot.AvatarWebPath}
	}
	key, ok := c.objectKey(raw)
	if !ok {
		return LegacyLink{Kind: LegacyUnknown}
	}
	if i := strings.IndexByte(key, '?'); i >= 0 {
		key = key[:i]
	}
	if kind, guildID, name, ok := sprobot.ParseFileKey(key); ok {
		return LegacyLink{Kind: LegacyFile, NewURL: c.webEndpoint + sprobot.FileWebPath(kind, guildID, name), GuildID: guildID}
	}
	if guildID, tmpl, userID, ok := sprobot.ParseProfileImageKey(key); ok {
		return LegacyLink{
			Kind:         LegacyProfileImage,
			NewURL:       c.webEndpoint + sprobot.ProfileImageWebPath(guildID, tmpl, userID, path.Ext(key)),
			GuildID:      guildID,
			TemplateName: tmpl,
			UserID:       userID,
		}
	}
	return LegacyLink{Kind: LegacyUnknown}
}

// sameProvider reports whether host and origin's host share their last two
// labels, e.g. both under linodeobjects.com.
func sameProvider(host, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	base := func(h string) string {
		labels := strings.Split(h, ".")
		if len(labels) < 2 || net.ParseIP(h) != nil {
			return h
		}
		return strings.Join(labels[len(labels)-2:], ".")
	}
	return host != "" && base(host) == base(u.Hostname())
}

// PresignFile presigns a GET for key that overrides the response's
// Content-Type and Content-Disposition, since stored objects carry no
// reliable type of their own.
func (c *Client) PresignFile(ctx context.Context, key string, ttl time.Duration, contentType, disposition string) (string, error) {
	out, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     &c.bucket,
		Key:                        &key,
		ResponseContentType:        &contentType,
		ResponseContentDisposition: &disposition,
	}, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("presigning %q: %w", key, err)
	}
	return out.URL, nil
}

// StatFile returns the size of an object in our bucket, or ErrNotFound.
func (c *Client) StatFile(ctx context.Context, key string) (int64, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("stat %q: %w", key, err)
	}
	if out.ContentLength == nil {
		return 0, nil
	}
	return *out.ContentLength, nil
}

// OpenImage streams a stored image from our bucket. The caller must close the
// returned body. Returns ErrNotFound for external URLs and missing objects.
func (c *Client) OpenImage(ctx context.Context, urlOrKey string) (body io.ReadCloser, contentLength int64, err error) {
	key, ok := c.objectKey(urlOrKey)
	if !ok {
		return nil, 0, ErrNotFound
	}
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("fetching image %q: %w", key, err)
	}
	var n int64
	if out.ContentLength != nil {
		n = *out.ContentLength
	}
	return out.Body, n, nil
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
	submitted := profile[tmpl.Image.Name]
	userErr, imageURL := c.getImageS3URL(ctx, tmpl, guildID, userID, profile)
	if imageURL != "" {
		profile[tmpl.Image.Name] = imageURL
		if imageURL != submitted {
			// Freshly re-hosted: bump the version so the stable image URL changes.
			profile[sprobot.ImageVersionKey] = strconv.FormatInt(time.Now().UnixMilli(), 36)
		}
	} else if userErr != "" {
		// Validation failed — preserve the previously saved image instead of
		// discarding it.
		existing, fetchErr := c.FetchProfile(ctx, tmpl, guildID, userID)
		if fetchErr == nil {
			if prev, ok := existing[tmpl.Image.Name]; ok && prev != "" {
				profile[tmpl.Image.Name] = prev
				profile[sprobot.ImageVersionKey] = existing[sprobot.ImageVersionKey]
			} else {
				delete(profile, tmpl.Image.Name)
			}
		} else {
			delete(profile, tmpl.Image.Name)
		}
	} else {
		delete(profile, tmpl.Image.Name)
	}
	if profile[sprobot.ImageVersionKey] == "" || profile[tmpl.Image.Name] == "" {
		delete(profile, sprobot.ImageVersionKey)
	}

	// Step 2: save the JSON profile
	if err := c.putProfile(ctx, tmpl, guildID, userID, profile); err != nil {
		return "", "", err
	}

	webURL = c.webEndpoint + sprobot.ProfileWebPath(guildID, tmpl.Name, userID)
	c.log.Info("Profile saved", "user_id", userID, "template", tmpl.Name, "guild_id", guildID, "profile_url", webURL)

	return webURL, userErr, nil
}

// SetProfileDisplayName changes only the stored display name of an existing
// profile. It returns ErrNotFound rather than creating one.
func (c *Client) SetProfileDisplayName(ctx context.Context, tmpl sprobot.Template, guildID, userID, name string) error {
	profile, err := c.FetchProfile(ctx, tmpl, guildID, userID)
	if err != nil {
		return err
	}
	if profile[sprobot.DisplayNameKey] == name {
		return nil
	}
	profile[sprobot.DisplayNameKey] = name
	return c.putProfile(ctx, tmpl, guildID, userID, profile)
}

func (c *Client) putProfile(ctx context.Context, tmpl sprobot.Template, guildID, userID string, profile map[string]string) error {
	s3Path := sprobot.ProfileS3Path(guildID, tmpl.Name, userID)
	body, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("marshaling profile: %w", err)
	}

	start := time.Now()
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Path,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return fmt.Errorf("saving profile to s3: %w", err)
	}
	c.log.Info(fmt.Sprintf("s3 write time: %dms", time.Since(start).Milliseconds()),
		"user_id", userID, "template", tmpl.Name, "guild_id", guildID)

	c.cache.Add(cacheKey(tmpl.Name, guildID, userID), profile)
	return nil
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

// DeleteKeys deletes each object key, skipping empty ones. Keys are bare S3
// keys, not URLs. A key that is already gone counts as deleted; every other
// failure is returned, joined, after all keys have been tried.
func (c *Client) DeleteKeys(ctx context.Context, keys ...string) error {
	var errs []error
	for _, key := range keys {
		if key == "" {
			continue
		}
		_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &c.bucket,
			Key:    &key,
		})
		if err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting %s from s3: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// saveRemoteFile downloads fileURL whole and stores it under
// {s3Prefix}/{guildID}/, returning the new key. Files over MaxFileSize fail
// with ErrFileTooLarge rather than being truncated.
func (c *Client) saveRemoteFile(ctx context.Context, guildID, fileURL, s3Prefix string) (string, error) {
	if err := urlValidator(fileURL); err != nil {
		return "", fmt.Errorf("URL validation failed: %w", err)
	}
	parsed, err := url.Parse(fileURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching file: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > fileSizeLimit {
		return "", fmt.Errorf("%w: %d bytes", ErrFileTooLarge, resp.ContentLength)
	}

	// Spool to disk rather than memory; a seekable body also lets the SDK sign
	// the payload, which a raw network stream would not.
	tmp, err := os.CreateTemp("", "sprobot-file-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, io.LimitReader(resp.Body, fileSizeLimit+1))
	if err != nil {
		return "", fmt.Errorf("downloading file: %w", err)
	}
	if size > fileSizeLimit {
		return "", fmt.Errorf("%w: over %d bytes", ErrFileTooLarge, fileSizeLimit)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding temp file: %w", err)
	}

	ext := sprobot.SafeFileExt(path.Ext(parsed.Path))
	contentType := sprobot.InlineContentType(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	s3Path := fmt.Sprintf("%s/%s/%s%s", s3Prefix, guildID, randomString(30), ext)
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &c.bucket,
		Key:           &s3Path,
		Body:          tmp,
		ContentLength: &size,
		ContentType:   &contentType,
	})
	if err != nil {
		return "", fmt.Errorf("uploading to s3: %w", err)
	}
	return s3Path, nil
}

func (c *Client) SaveModFile(ctx context.Context, guildID string, fileURL string) (string, error) {
	c.log.Info("Saving file to mod log", "guild_id", guildID)
	s3Path, err := c.saveRemoteFile(ctx, guildID, fileURL, "mod_files")
	if err != nil {
		return "", err
	}
	c.log.Info("Mod file saved", "guild_id", guildID, "s3_key", s3Path)
	return s3Path, nil
}

func (c *Client) SaveShortcutImage(ctx context.Context, guildID string, fileURL string) (string, error) {
	c.log.Info("Saving shortcut image", "guild_id", guildID)
	if strings.HasPrefix(fileURL, c.endpoint) || !strings.HasPrefix(fileURL, "http") {
		return fileURL, nil
	}
	s3Path, err := c.saveRemoteFile(ctx, guildID, fileURL, "shortcut_files")
	if err != nil {
		return "", err
	}
	c.log.Info("Shortcut image saved", "guild_id", guildID, "s3_key", s3Path)
	return s3Path, nil
}

func (c *Client) getImageS3URL(ctx context.Context, tmpl sprobot.Template, guildID, userID string, profile map[string]string) (userErr string, s3URL string) {
	maybeURL, ok := profile[tmpl.Image.Name]
	if !ok || maybeURL == "" {
		return "", ""
	}

	if strings.HasPrefix(maybeURL, c.endpoint) || !strings.HasPrefix(maybeURL, "http") {
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
	})
	if err != nil {
		return "Unable to save image. The rest of your profile has been saved.", ""
	}

	c.log.Info("Profile image saved", "user_id", userID, "template", tmpl.Name, "guild_id", guildID, "s3_key", s3Path)
	return "", s3Path
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

func (c *Client) FetchHiddenThreads(ctx context.Context, guildID string) ([]byte, error) {
	return c.FetchGuildJSON(ctx, "threadhidden", guildID)
}

func (c *Client) SaveHiddenThreads(ctx context.Context, guildID string, data []byte) error {
	return c.SaveGuildJSON(ctx, "threadhidden", guildID, data)
}

func (c *Client) SaveStickyFile(ctx context.Context, guildID, fileURL string) (string, error) {
	return c.saveRemoteFile(ctx, guildID, fileURL, "sticky_files")
}

// randomString returns n characters from crypto/rand. Saved-file names are the
// only thing keeping their permanent links from being guessed.
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	limit := big.NewInt(int64(len(letters)))
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(fmt.Sprintf("crypto/rand failed: %v", err))
		}
		b[i] = letters[idx.Int64()]
	}
	return string(b)
}
