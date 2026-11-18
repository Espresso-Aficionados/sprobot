package s3client

import "testing"

func TestBucketHostLayout(t *testing.T) {
	tests := []struct {
		endpoint, bucket         string
		wantEndpoint, wantPrefix string
	}{
		{"https://profile-bot.us-southeast-1.linodeobjects.com/", "profile-bot", "https://us-southeast-1.linodeobjects.com", "profile-bot/"},
		{"https://profile-bot-dev.us-southeast-1.linodeobjects.com", "profile-bot-dev", "https://us-southeast-1.linodeobjects.com", "profile-bot-dev/"},
		{"http://test-bucket.s3.test:9000/", "test-bucket", "http://s3.test:9000", "test-bucket/"},
		// Not the bucket's hostname.
		{"https://us-southeast-1.linodeobjects.com/", "profile-bot", "", ""},
		{"https://profile-bot-dev.us-southeast-1.linodeobjects.com/", "profile-bot", "", ""},
		{"http://127.0.0.1:9000", "test-bucket", "", ""},
		{"https://profile-bot.example.com/some/path", "profile-bot", "", ""},
		{"https://profile-bot.example.com/", "", "", ""},
		{"://bad", "profile-bot", "", ""},
	}
	for _, tt := range tests {
		gotEndpoint, gotPrefix := bucketHostLayout(tt.endpoint, tt.bucket)
		if gotEndpoint != tt.wantEndpoint || gotPrefix != tt.wantPrefix {
			t.Errorf("bucketHostLayout(%q, %q) = %q, %q; want %q, %q", tt.endpoint, tt.bucket, gotEndpoint, gotPrefix, tt.wantEndpoint, tt.wantPrefix)
		}
	}
}
