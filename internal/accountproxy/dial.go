package accountproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type DialProvider interface {
	DialContext(
		ctx context.Context,
		accountID int64,
	) (func(context.Context, string, string) (net.Conn, error), error)
}

func Resolve(
	provider DialProvider,
	ctx context.Context,
	accountID int64,
) (func(context.Context, string, string) (net.Conn, error), error) {
	if provider == nil || accountID <= 0 {
		return nil, nil
	}
	dial, err := provider.DialContext(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("resolve account %d proxy: %w", accountID, err)
	}
	return dial, nil
}

var CheckTimeout = 8 * time.Second

var tgDCAddresses = []string{
	"149.154.167.51:443",
	"149.154.175.53:443",
	"91.108.56.130:443",
}

func Check(ctx context.Context, profile Profile) error {
	dialer, err := NewDialer(profile)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	var failures []error
	for _, target := range tgDCAddresses {
		connection, err := dialer.DialContext(checkCtx, "tcp", target)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
