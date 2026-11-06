// Package opsecrets reads secrets from the environment. A value that is a
// 1Password secret reference (op://vault/item/field) is resolved through the
// 1Password SDK using the service account in OP_SERVICE_ACCOUNT_TOKEN. Any
// other value is returned as-is, so plain values keep working.
//
// Resolved secrets are cached for the life of the process, and GetenvAll
// resolves several references in one request. Service accounts share a daily
// request quota per account, so read secrets once at startup and batch them
// where the call sites allow it.
package opsecrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	onepassword "github.com/1password/onepassword-sdk-go"
)

const (
	refPrefix   = "op://"
	tokenEnvVar = "OP_SERVICE_ACCOUNT_TOKEN"
	timeout     = 30 * time.Second
)

// resolve fetches secret references in a single request. A failed reference
// is reported in the returned error and omitted from the map. Replaced in
// tests.
var resolve = resolveWithSDK

var (
	mu    sync.Mutex
	cache = map[string]string{} // reference -> secret
)

// client is created on first use, so a process with no op:// values never
// loads the SDK.
var client = sync.OnceValues(func() (*onepassword.Client, error) {
	token := os.Getenv(tokenEnvVar)
	if token == "" {
		return nil, fmt.Errorf("%s is not set", tokenEnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo("opsecrets", onepassword.DefaultIntegrationVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("creating 1Password client: %w", err)
	}
	return c, nil
})

// resolveWithSDK uses ResolveAll even for one reference: it costs one
// request against the rate limit, where Resolve costs two.
func resolveWithSDK(refs []string) (map[string]string, error) {
	c, err := client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := c.Secrets().ResolveAll(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	var errs []error
	for _, ref := range refs {
		r, ok := res.IndividualResponses[ref]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s: no response", ref))
		case r.Error != nil:
			errs = append(errs, fmt.Errorf("%s: %s", ref, r.Error.Type))
		case r.Content == nil:
			errs = append(errs, fmt.Errorf("%s: empty response", ref))
		default:
			out[ref] = r.Content.Secret
		}
	}
	return out, errors.Join(errs...)
}

// Getenv returns the named environment variable, resolving it through
// 1Password when it holds an op:// reference. An unset variable returns "".
func Getenv(name string) (string, error) {
	vals, err := GetenvAll(name)
	if err != nil {
		return "", err
	}
	return vals[name], nil
}

// GetenvAll is Getenv for several variables, resolving all their uncached
// op:// references in one request. The result maps each name to its value.
func GetenvAll(names ...string) (map[string]string, error) {
	mu.Lock()
	defer mu.Unlock()

	vals := make(map[string]string, len(names))
	var pending []string
	for _, name := range names {
		v := os.Getenv(name)
		vals[name] = v
		if !strings.HasPrefix(v, refPrefix) {
			continue
		}
		if _, ok := cache[v]; !ok && !slices.Contains(pending, v) {
			pending = append(pending, v)
		}
	}

	var resolveErr error
	if len(pending) > 0 {
		resolved, err := resolve(pending)
		for ref, secret := range resolved {
			cache[ref] = secret
		}
		resolveErr = err
	}

	var errs []error
	for _, name := range names {
		ref := vals[name]
		if !strings.HasPrefix(ref, refPrefix) {
			continue
		}
		secret, ok := cache[ref]
		if !ok {
			errs = append(errs, fmt.Errorf("resolving %s (%s) from 1Password", name, ref))
			continue
		}
		vals[name] = secret
	}
	if len(errs) > 0 {
		if resolveErr != nil {
			errs = append(errs, resolveErr)
		}
		return nil, errors.Join(errs...)
	}
	return vals, nil
}
