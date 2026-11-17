package telegramauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type GotdDriver struct {
	appID   int
	appHash string
}

func NewGotdDriver(appID int, appHash string) *GotdDriver {
	return &GotdDriver{
		appID:   appID,
		appHash: strings.TrimSpace(appHash),
	}
}

func (d *GotdDriver) SendCode(
	ctx context.Context,
	sessionPath string,
	phone string,
) (SentCode, error) {
	var result SentCode
	err := d.withClient(ctx, sessionPath, func(ctx context.Context, client *telegram.Client) error {
		sent, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			return mapTelegramError(err)
		}
		value, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("%w: unexpected sent code response %T", ErrTelegram, sent)
		}
		result = SentCode{
			CodeHash:   value.PhoneCodeHash,
			CodeType:   sentCodeType(value.Type),
			CodeLength: sentCodeLength(value.Type),
		}
		return nil
	})
	return result, err
}

func (d *GotdDriver) SignIn(
	ctx context.Context,
	sessionPath string,
	phone string,
	code string,
	codeHash string,
) (*Profile, bool, error) {
	var profile *Profile
	passwordRequired := false
	err := d.withClient(ctx, sessionPath, func(ctx context.Context, client *telegram.Client) error {
		if _, err := client.Auth().SignIn(ctx, phone, code, codeHash); err != nil {
			if errors.Is(err, auth.ErrPasswordAuthNeeded) {
				passwordRequired = true
				return nil
			}
			return mapTelegramError(err)
		}
		current, err := profileFromClient(ctx, client)
		if err != nil {
			return err
		}
		profile = current
		return nil
	})
	return profile, passwordRequired, err
}

func (d *GotdDriver) Password(
	ctx context.Context,
	sessionPath string,
	password string,
) (*Profile, error) {
	var profile *Profile
	err := d.withClient(ctx, sessionPath, func(ctx context.Context, client *telegram.Client) error {
		if _, err := client.Auth().Password(ctx, password); err != nil {
			return mapTelegramError(err)
		}
		current, err := profileFromClient(ctx, client)
		if err != nil {
			return err
		}
		profile = current
		return nil
	})
	return profile, err
}

func (d *GotdDriver) withClient(
	ctx context.Context,
	sessionPath string,
	run func(context.Context, *telegram.Client) error,
) error {
	if err := PrepareDeviceSession(sessionPath); err != nil {
		return fmt.Errorf("prepare Telegram device: %w", err)
	}
	client := telegram.NewClient(d.appID, d.appHash, telegram.Options{
		SessionStorage:  &session.FileStorage{Path: sessionPath},
		Device:          DeviceForSession(sessionPath),
		DialTimeout:     15 * time.Second,
		ExchangeTimeout: 15 * time.Second,
	})
	if err := client.Run(ctx, func(ctx context.Context) error {
		return run(ctx, client)
	}); err != nil {
		return fmt.Errorf("Telegram client: %w", err)
	}
	return nil
}

func profileFromClient(ctx context.Context, client *telegram.Client) (*Profile, error) {
	self, err := client.Self(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: load current Telegram user: %v", ErrTelegram, err)
	}
	firstName, _ := self.GetFirstName()
	lastName, _ := self.GetLastName()
	username, _ := self.GetUsername()
	phone, _ := self.GetPhone()
	displayName := strings.TrimSpace(strings.Join([]string{firstName, lastName}, " "))
	if displayName == "" {
		displayName = username
	}
	return &Profile{
		ID:          self.GetID(),
		PhoneNumber: phone,
		Username:    username,
		DisplayName: displayName,
	}, nil
}

func mapTelegramError(err error) error {
	switch {
	case errors.Is(err, auth.ErrPasswordInvalid), tgerr.Is(err, tg.ErrPasswordHashInvalid):
		return ErrInvalidPassword
	case tgerr.Is(err, tg.ErrPhoneCodeInvalid):
		return ErrInvalidCode
	case tgerr.Is(err, tg.ErrPhoneCodeExpired):
		return ErrCodeExpired
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &FloodWaitError{Duration: wait}
	}
	return fmt.Errorf("%w: %v", ErrTelegram, err)
}

func sentCodeType(codeType tg.AuthSentCodeTypeClass) string {
	switch codeType.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "Telegram 应用内验证码"
	case *tg.AuthSentCodeTypeSMS:
		return "短信验证码"
	case *tg.AuthSentCodeTypeCall:
		return "语音验证码"
	case *tg.AuthSentCodeTypeFlashCall:
		return "闪信电话验证码"
	case *tg.AuthSentCodeTypeMissedCall:
		return "未接来电验证码"
	case *tg.AuthSentCodeTypeEmailCode:
		return "邮箱验证码"
	default:
		return "Telegram 验证码"
	}
}

func sentCodeLength(codeType tg.AuthSentCodeTypeClass) int {
	switch value := codeType.(type) {
	case *tg.AuthSentCodeTypeApp:
		return value.Length
	case *tg.AuthSentCodeTypeSMS:
		return value.Length
	case *tg.AuthSentCodeTypeCall:
		return value.Length
	case *tg.AuthSentCodeTypeEmailCode:
		return value.Length
	default:
		return 0
	}
}
