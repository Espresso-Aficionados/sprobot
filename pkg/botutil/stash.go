package botutil

import (
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// MessageStashTTL is how long a stashed message is kept. Modal interaction
// tokens expire after 15 minutes, so no valid submit can arrive for an entry
// older than this.
const MessageStashTTL = 20 * time.Minute

// MessageStash holds a message context-menu target between the modal opening
// and its submit. The resolved target includes content without the privileged
// message content intent, and it survives the message being deleted while the
// modal is open; a REST re-fetch at submit time gets neither. The zero value is
// ready to use.
type MessageStash struct {
	mu      sync.Mutex
	entries map[snowflake.ID]stashedMessage
}

type stashedMessage struct {
	msg      discord.Message
	storedAt time.Time
}

func (s *MessageStash) Put(msg discord.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if s.entries == nil {
		s.entries = make(map[snowflake.ID]stashedMessage)
	}
	s.entries[msg.ID] = stashedMessage{msg: msg, storedAt: time.Now()}
}

// Get returns the stashed message, if fresh. Entries are read
// non-destructively so concurrent modals for the same message and duplicate
// submits all resolve; pruning handles cleanup.
func (s *MessageStash) Get(messageID snowflake.ID) (discord.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	e, ok := s.entries[messageID]
	return e.msg, ok
}

func (s *MessageStash) pruneLocked() {
	for id, e := range s.entries {
		if time.Since(e.storedAt) > MessageStashTTL {
			delete(s.entries, id)
		}
	}
}
