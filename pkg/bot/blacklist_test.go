package bot

import (
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name string
		list []snowflake.ID
		ids  []snowflake.ID
		want bool
	}{
		{"empty list", nil, []snowflake.ID{1}, false},
		{"empty ids", []snowflake.ID{1, 2, 3}, nil, false},
		{"match", []snowflake.ID{1, 2, 3}, []snowflake.ID{2}, true},
		{"no match", []snowflake.ID{1, 2, 3}, []snowflake.ID{4, 5}, false},
		{"variadic partial match", []snowflake.ID{1, 2, 3}, []snowflake.ID{4, 3}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsAny(tt.list, tt.ids...)
			if got != tt.want {
				t.Errorf("containsAny(%v, %v) = %v, want %v", tt.list, tt.ids, got, tt.want)
			}
		})
	}
}
