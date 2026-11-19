package telegramchat

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type MessageCollection struct {
	Items     []Message `json:"items"`
	NewestID  int       `json:"newest_id"`
	OldestID  int       `json:"oldest_id"`
	Complete  bool      `json:"complete"`
	Backlog   bool      `json:"backlog"`
	PagesRead int       `json:"pages_read"`
}

// CollectMessages reads recent history until checkpointID is reached. A zero
// checkpoint establishes a baseline and intentionally returns no messages.
func (s *Service) CollectMessages(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	checkpointID int,
	pageSize int,
	maxPages int,
) (MessageCollection, error) {
	if s == nil {
		return MessageCollection{}, ErrNotConfigured
	}
	if peerID <= 0 || checkpointID < 0 || pageSize <= 0 || maxPages <= 0 {
		return MessageCollection{}, ErrPeerNotFound
	}
	pageSize = normalizeLimit(pageSize)
	var collection MessageCollection
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		inputPeer, err := s.resolveInputPeer(ctx, client.API(), accountID, peerType, peerID)
		if err != nil {
			return err
		}
		collection, err = collectMessagePages(
			checkpointID,
			pageSize,
			maxPages,
			func(beforeID, limit int) (MessagePage, error) {
				result, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
					Peer:     inputPeer,
					OffsetID: beforeID,
					Limit:    limit,
				})
				if err != nil {
					return MessagePage{}, err
				}
				s.cacheMessagesResult(accountID, result, time.Now())
				return mapMessages(result, limit)
			},
		)
		return err
	})
	if err != nil {
		return collection, err
	}
	return collection, nil
}

func collectMessagePages(
	checkpointID int,
	pageSize int,
	maxPages int,
	fetch func(beforeID, limit int) (MessagePage, error),
) (MessageCollection, error) {
	collection := MessageCollection{}
	if checkpointID == 0 {
		page, err := fetch(0, pageSize)
		if err != nil {
			return MessageCollection{}, fmt.Errorf("read Telegram message baseline: %w", err)
		}
		collection.PagesRead = 1
		collection.NewestID = newestMessageID(page.Items)
		collection.Complete = true
		return collection, nil
	}

	seen := make(map[int]struct{})
	beforeID := 0
	for collection.PagesRead < maxPages {
		page, err := fetch(beforeID, pageSize)
		if err != nil {
			collection.Backlog = true
			return collection, fmt.Errorf("read Telegram message history: %w", err)
		}
		collection.PagesRead++
		if len(page.Items) == 0 {
			collection.Complete = true
			break
		}
		reachedCheckpoint := false
		oldestID := 0
		for _, message := range page.Items {
			if message.ID <= 0 {
				continue
			}
			if oldestID == 0 || message.ID < oldestID {
				oldestID = message.ID
			}
			if message.ID > collection.NewestID {
				collection.NewestID = message.ID
			}
			if collection.OldestID == 0 || message.ID < collection.OldestID {
				collection.OldestID = message.ID
			}
			if message.ID <= checkpointID {
				reachedCheckpoint = true
			}
			if _, exists := seen[message.ID]; exists {
				continue
			}
			seen[message.ID] = struct{}{}
			collection.Items = append(collection.Items, message)
		}
		if reachedCheckpoint {
			collection.Complete = true
			break
		}
		nextBeforeID := page.NextBeforeID
		if nextBeforeID <= 0 {
			nextBeforeID = oldestID
		}
		if nextBeforeID <= 0 || nextBeforeID == beforeID {
			collection.Backlog = true
			return collection, fmt.Errorf(
				"Telegram message pagination did not advance from message %d",
				beforeID,
			)
		}
		beforeID = nextBeforeID
	}
	sort.SliceStable(collection.Items, func(i, j int) bool {
		return collection.Items[i].ID < collection.Items[j].ID
	})
	collection.Backlog = !collection.Complete
	return collection, nil
}

func newestMessageID(messages []Message) int {
	newest := 0
	for _, message := range messages {
		if message.ID > newest {
			newest = message.ID
		}
	}
	return newest
}
