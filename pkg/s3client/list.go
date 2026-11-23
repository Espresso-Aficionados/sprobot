package s3client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Object is one entry of a bucket listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListObjects returns every object whose key starts with prefix, reading the
// bucket listing a page (up to 1000 keys) at a time.
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]Object, error) {
	var objects []Object
	stored := c.keyPrefix + prefix
	pages := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: &stored,
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx, func(o *s3.Options) {
			// See bucketHostLayout: the configured endpoint can't list
			// path-style, so the listing is virtual-hosted instead: the
			// region's endpoint as the base, which the SDK addresses as
			// {bucket}.{host}, the bucket's own hostname again. Never the
			// region's bare hostname: through that, path-style requests
			// reach any bucket in the region, so the egress proxy allows
			// only the bucket's own.
			if c.bucketEndpoint != "" {
				o.BaseEndpoint = &c.bucketEndpoint
				o.UsePathStyle = false
			}
		})
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			if o.Key == nil {
				continue
			}
			obj := Object{Key: strings.TrimPrefix(*o.Key, c.keyPrefix)}
			if o.Size != nil {
				obj.Size = *o.Size
			}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			objects = append(objects, obj)
		}
	}
	return objects, nil
}
