package stickybot

import (
	"fmt"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
)

// The sticky settings modal's custom ID is labelled by its prefix, not its
// channel and message IDs.
func TestModalLabel(t *testing.T) {
	customID := fmt.Sprintf("%s%d_%d", modalPrefix, 1013566342345019512, 1300000000000000000)
	if got := botutil.CustomIDLabel(customID, modalNames); got != "sticky_config" {
		t.Errorf("modal %q labelled %q, want sticky_config", customID, got)
	}
}
