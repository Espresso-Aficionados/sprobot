package s3client

import (
	"context"
	"fmt"
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
	pages := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: &prefix,
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			if o.Key == nil {
				continue
			}
			obj := Object{Key: *o.Key}
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
