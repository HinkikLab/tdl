package chat

import (
	"context"
	"fmt"
	"math"

	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// ArchiveWindow selects source messages before applying tags or following links.
// ID ranges are [StartID, EndID); timestamp windows are [Since, Until].
// An album is selected when any member belongs to the window, and stays intact.
type ArchiveWindow struct {
	StartID, EndID int
	Since, Until   int64
}

func (w ArchiveWindow) Validate() error {
	if w.StartID < 0 || w.EndID < 0 || (w.EndID > 0 && w.EndID <= w.StartID) {
		return diagnostic.InvalidRange(w.StartID, w.EndID)
	}
	if w.Since < 0 || w.Until < 0 || (w.Until > 0 && w.Until < w.Since) {
		return diagnostic.New("errors.batch.invalid_timestamp_window", map[string]any{"Start": w.Since, "End": w.Until})
	}
	return nil
}

func (w ArchiveWindow) contains(id, date int) bool {
	return (w.StartID == 0 || id >= w.StartID) && (w.EndID == 0 || id < w.EndID) &&
		(w.Since == 0 || int64(date) >= w.Since) && (w.Until == 0 || int64(date) <= w.Until)
}

func (w ArchiveWindow) matches(album []*tg.Message) bool {
	for _, m := range album {
		if w.contains(m.ID, m.Date) {
			return true
		}
	}
	return false
}

// scanArchiveHistory groups before filtering so boundaries never split albums.
// A false callback result means a preview limit left the window incomplete.
func scanArchiveHistory(ctx context.Context, history messages.Query, window ArchiveWindow,
	visit func([]*tg.Message) (bool, error),
) (complete bool, err error) {
	if err := window.Validate(); err != nil {
		return false, err
	}
	it := messages.NewIterator(history, 100)
	if window.EndID > 0 {
		// Telegram albums have at most ten members. Include members above end.
		it = it.OffsetID(min(window.EndID, math.MaxInt32-10) + 10)
	}
	var pending []*tg.Message
	scanned := 0
	flush := func() (bool, error) {
		album := pending
		pending = nil
		if len(album) == 0 || !window.matches(album) {
			return true, nil
		}
		return visit(album)
	}
	for it.Next(ctx) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		m, ok := it.Value().Msg.(*tg.Message)
		if !ok || (len(pending) > 0 && (m.GroupedID == 0 || pending[0].GroupedID != m.GroupedID)) {
			if more, err := flush(); err != nil || !more {
				return false, err
			}
		}
		if !ok {
			continue
		}
		if len(pending) == 0 && ((window.StartID > 0 && m.ID < window.StartID) || (window.Since > 0 && int64(m.Date) < window.Since)) {
			break
		}
		scanned++
		if window.Until > 0 && scanned > 100000 {
			return false, diagnostic.Describe(fmt.Errorf("incremental archive scan exceeded 100000 messages; keeping last_ts"), corei18n.Message{ID: "errors.message.incremental_archive_scan_exceeded_100000_messages_keeping_last_key_ts"})
		}
		pending = append(pending, m)
		if m.GroupedID == 0 {
			if more, err := flush(); err != nil || !more {
				return false, err
			}
		}
	}
	if err := it.Err(); err != nil {
		return false, diagnostic.Describe(fmt.Errorf("scan chat history: %w", err), corei18n.Message{ID: "errors.message.scan_chat_history_value", Args: map[string]any{"Arg1": err}})
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	more, err := flush()
	return more && err == nil, err
}
