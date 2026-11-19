package telegramchat

import (
	"errors"
	"testing"
)

func TestCollectMessagePagesCatchesUpToCheckpoint(t *testing.T) {
	pages := map[int]MessagePage{
		0: {
			Items:        messageRange(200, 151),
			NextBeforeID: 151,
		},
		151: {
			Items:        messageRange(150, 101),
			NextBeforeID: 101,
		},
		101: {
			Items: messageRange(100, 99),
		},
	}
	var offsets []int
	collection, err := collectMessagePages(
		100,
		50,
		20,
		func(beforeID, _ int) (MessagePage, error) {
			offsets = append(offsets, beforeID)
			return pages[beforeID], nil
		},
	)
	if err != nil {
		t.Fatalf("collectMessagePages() error = %v", err)
	}
	if !collection.Complete || collection.Backlog {
		t.Fatalf("collection completion = complete:%t backlog:%t", collection.Complete, collection.Backlog)
	}
	if collection.NewestID != 200 || collection.OldestID != 99 {
		t.Fatalf("collection range = %d..%d, want 99..200", collection.OldestID, collection.NewestID)
	}
	if len(collection.Items) != 102 {
		t.Fatalf("collection items = %d, want 102", len(collection.Items))
	}
	if len(offsets) != 3 || offsets[0] != 0 || offsets[1] != 151 || offsets[2] != 101 {
		t.Fatalf("history offsets = %v, want [0 151 101]", offsets)
	}
	if collection.Items[0].ID != 99 || collection.Items[len(collection.Items)-1].ID != 200 {
		t.Fatalf("collection was not ordered chronologically")
	}
}

func TestCollectMessagePagesMarksCappedCatchupAsBacklog(t *testing.T) {
	collection, err := collectMessagePages(
		100,
		50,
		1,
		func(beforeID, _ int) (MessagePage, error) {
			if beforeID != 0 {
				t.Fatalf("unexpected second page offset %d", beforeID)
			}
			return MessagePage{
				Items:        messageRange(200, 151),
				NextBeforeID: 151,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("collectMessagePages() error = %v", err)
	}
	if collection.Complete || !collection.Backlog {
		t.Fatalf("capped collection = complete:%t backlog:%t", collection.Complete, collection.Backlog)
	}
	if collection.PagesRead != 1 || collection.NewestID != 200 || collection.OldestID != 151 {
		t.Fatalf("capped collection = %+v", collection)
	}
}

func TestCollectMessagePagesEstablishesBaselineWithoutIndexingHistory(t *testing.T) {
	collection, err := collectMessagePages(
		0,
		50,
		20,
		func(beforeID, _ int) (MessagePage, error) {
			if beforeID != 0 {
				t.Fatalf("baseline offset = %d, want 0", beforeID)
			}
			return MessagePage{Items: messageRange(300, 251)}, nil
		},
	)
	if err != nil {
		t.Fatalf("collectMessagePages() error = %v", err)
	}
	if !collection.Complete || collection.Backlog || collection.NewestID != 300 {
		t.Fatalf("baseline collection = %+v", collection)
	}
	if len(collection.Items) != 0 {
		t.Fatalf("baseline returned %d historical messages", len(collection.Items))
	}
}

func TestCollectMessagePagesPreservesBacklogOnPaginationFailure(t *testing.T) {
	requests := 0
	collection, err := collectMessagePages(
		100,
		50,
		20,
		func(beforeID, _ int) (MessagePage, error) {
			requests++
			if requests == 1 {
				return MessagePage{
					Items:        messageRange(200, 151),
					NextBeforeID: 151,
				}, nil
			}
			return MessagePage{}, errors.New("Telegram unavailable")
		},
	)
	if err == nil {
		t.Fatal("collectMessagePages() accepted a pagination failure")
	}
	if !collection.Backlog || collection.PagesRead != 1 || collection.NewestID != 200 {
		t.Fatalf("failed collection = %+v", collection)
	}
}

func messageRange(newest, oldest int) []Message {
	items := make([]Message, 0, newest-oldest+1)
	for id := newest; id >= oldest; id-- {
		items = append(items, Message{ID: id})
	}
	return items
}
