package telegramchat

import (
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestAvatarLocationUsesSmallestUsefulThumbnail(t *testing.T) {
	photo := &tg.Photo{
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 64, H: 64, Size: 100},
			&tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 1_000},
			&tg.PhotoSize{Type: "x", W: 800, H: 800, Size: 5_000},
		},
	}

	location, err := avatarLocation(photo)
	if err != nil {
		t.Fatalf("avatarLocation() error = %v", err)
	}
	if location.ThumbSize != "m" {
		t.Fatalf("avatar thumbnail = %q, want m", location.ThumbSize)
	}
}

func TestAvatarLocationRejectsEmptyPhotos(t *testing.T) {
	_, err := avatarLocation(&tg.Photo{
		Sizes: []tg.PhotoSizeClass{&tg.PhotoSizeEmpty{}},
	})
	if !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("avatarLocation() error = %v, want ErrNoAvatar", err)
	}
}

func TestNewAvatarRejectsEmptyResponse(t *testing.T) {
	_, err := newAvatar(nil)
	if !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("newAvatar(nil) error = %v, want ErrNoAvatar", err)
	}
}

func TestNewAvatarAcceptsImagePayload(t *testing.T) {
	avatar, err := newAvatar([]byte("\x89PNG\r\n\x1a\n"))
	if err != nil {
		t.Fatalf("newAvatar() error = %v", err)
	}
	if avatar.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", avatar.ContentType)
	}
}

func TestCacheMessagesResultCachesPeerIdentity(t *testing.T) {
	service := &Service{peers: make(map[string]cachedPeer)}
	user := &tg.User{ID: 42, AccessHash: 7}
	user.SetFlags()
	result := &tg.MessagesChannelMessages{
		Users: []tg.UserClass{user},
	}

	service.cacheMessagesResult(9, result, time.Now())

	peer, ok := service.readPeer(9, "user", 42, time.Now())
	if !ok {
		t.Fatal("message user was not cached")
	}
	if got := peer.(*tg.InputPeerUser).AccessHash; got != 7 {
		t.Fatalf("cached access hash = %d, want 7", got)
	}
}
