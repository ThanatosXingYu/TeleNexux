// Package notifications delivers queued monitor alerts to external channels.
package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
)

const (
	deliveryBatchSize = 20
	webhookTimeout    = 12 * time.Second
	maxResponseBytes  = 4 << 10
)

type DeliveryStore interface {
	ClaimNotificationDelivery(context.Context) (operations.NotificationDelivery, bool, error)
	CompleteNotificationDelivery(context.Context, int64, int) error
	RetryNotificationDelivery(
		context.Context,
		int64,
		int,
		string,
	) (string, *time.Time, error)
}

type Options struct {
	Interval     time.Duration
	AllowPrivate bool
	Client       *http.Client
}

type Status struct {
	Running      bool
	LastStarted  *time.Time
	LastFinished *time.Time
	LastError    string
}

type Service struct {
	store        DeliveryStore
	client       *http.Client
	interval     time.Duration
	allowPrivate bool
	logger       *slog.Logger
	runMu        sync.Mutex
	mu           sync.Mutex
	status       Status
}

func New(store DeliveryStore, options Options, logger *slog.Logger) *Service {
	interval := options.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	client := options.Client
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:        store,
		client:       client,
		interval:     interval,
		allowPrivate: options.AllowPrivate,
		logger:       logger,
	}
}

func (s *Service) Start(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn("notification delivery run failed", "error", err)
				}
				timer.Reset(s.interval)
			}
		}
	}()
}

func (s *Service) RunOnce(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("notification service is not configured")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	started := time.Now().UTC()
	s.setRunning(true, started)
	var runErr error
	for processed := 0; processed < deliveryBatchSize; processed++ {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		delivery, found, err := s.store.ClaimNotificationDelivery(ctx)
		if err != nil {
			runErr = err
			break
		}
		if !found {
			break
		}
		if err := s.sendWebhook(ctx, delivery); err != nil {
			state, nextAttempt, retryErr := s.store.RetryNotificationDelivery(
				context.WithoutCancel(ctx),
				delivery.ID,
				delivery.AttemptCount,
				err.Error(),
			)
			if retryErr != nil {
				runErr = errors.Join(runErr, retryErr)
				continue
			}
			s.logger.Warn(
				"notification delivery failed",
				"delivery_id", delivery.ID,
				"channel_id", delivery.ChannelID,
				"attempt", delivery.AttemptCount,
				"state", state,
				"next_attempt_at", nextAttempt,
			)
			continue
		}
		if err := s.store.CompleteNotificationDelivery(
			context.WithoutCancel(ctx),
			delivery.ID,
			delivery.AttemptCount,
		); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	s.finish(started, runErr)
	return runErr
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) sendWebhook(
	ctx context.Context,
	delivery operations.NotificationDelivery,
) error {
	if delivery.ChannelKind != operations.NotificationKindWebhook {
		return errors.New("通知渠道类型不受支持")
	}
	var config operations.WebhookChannelConfig
	if err := json.Unmarshal([]byte(delivery.ChannelConfig), &config); err != nil {
		return errors.New("Webhook 渠道配置无效")
	}
	destination, addresses, err := s.resolveDestination(ctx, config.URL)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(webhookPayload{
		Event:          "monitor.alert",
		DeliveryID:     delivery.ID,
		AlertID:        delivery.AlertID,
		RuleID:         delivery.RuleID,
		RuleName:       delivery.RuleName,
		AccountID:      delivery.AccountID,
		PeerType:       delivery.PeerType,
		PeerID:         delivery.PeerID,
		PeerTitle:      delivery.PeerTitle,
		MessageID:      delivery.MessageID,
		MatchedKeyword: delivery.MatchedKeyword,
		MessagePreview: delivery.MessagePreview,
		Severity:       delivery.Severity,
		CreatedAt:      delivery.AlertCreatedAt,
	})
	if err != nil {
		return errors.New("无法生成 Webhook 请求")
	}
	requestCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		destination.String(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return errors.New("Webhook 请求无效")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "TeleNexus-Webhook/1")
	request.Header.Set("X-TeleNexus-Delivery", strconv.FormatInt(delivery.ID, 10))
	request.Header.Set("X-TeleNexus-Attempt", strconv.Itoa(delivery.AttemptCount))
	if config.Secret != "" {
		mac := hmac.New(sha256.New, []byte(config.Secret))
		_, _ = mac.Write(payload)
		request.Header.Set("X-TeleNexus-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := s.client
	if client == nil {
		client = webhookHTTPClient(addresses)
		defer client.CloseIdleConnections()
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Webhook 网络请求失败")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Webhook 返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Service) validateDestination(ctx context.Context, rawURL string) error {
	_, _, err := s.resolveDestination(ctx, rawURL)
	return err
}

func (s *Service) resolveDestination(
	ctx context.Context,
	rawURL string,
) (*url.URL, []net.IPAddr, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return nil, nil, errors.New("Webhook 地址无效")
	}
	if parsed.Scheme != "https" && !(s.allowPrivate && parsed.Scheme == "http") {
		return nil, nil, errors.New("Webhook 必须使用 HTTPS")
	}
	if port := parsed.Port(); port != "" && port != "443" && !s.allowPrivate {
		return nil, nil, errors.New("Webhook 端口不受支持")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, nil, errors.New("Webhook 域名无法解析")
	}
	if s.allowPrivate {
		return parsed, addresses, nil
	}
	for _, address := range addresses {
		if unsafeIP(address.IP) {
			return nil, nil, errors.New("Webhook 地址不得指向本机或私有网络")
		}
	}
	return parsed, addresses, nil
}

func webhookHTTPClient(addresses []net.IPAddr) *http.Client {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(
		ctx context.Context,
		network string,
		address string,
	) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook destination port: %w", err)
		}
		var dialErr error
		for _, resolved := range addresses {
			conn, err := dialer.DialContext(
				ctx,
				network,
				net.JoinHostPort(resolved.IP.String(), port),
			)
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
		}
		if dialErr == nil {
			dialErr = errors.New("webhook destination has no addresses")
		}
		return nil, fmt.Errorf("dial webhook destination: %w", dialErr)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   webhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func unsafeIP(ip net.IP) bool {
	if ip == nil ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	ipv4 := ip.To4()
	return ipv4 != nil &&
		((ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127) ||
			(ipv4[0] == 192 && ipv4[1] == 0 && ipv4[2] == 0) ||
			(ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19)) ||
			(ipv4[0] == 198 && ipv4[1] == 51 && ipv4[2] == 100) ||
			(ipv4[0] == 203 && ipv4[1] == 0 && ipv4[2] == 113))
}

func (s *Service) setRunning(running bool, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = running
	s.status.LastStarted = &started
}

func (s *Service) finish(started time.Time, runErr error) {
	finished := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	s.status.LastStarted = &started
	s.status.LastFinished = &finished
	s.status.LastError = ""
	if runErr != nil {
		s.status.LastError = strings.TrimSpace(runErr.Error())
	}
}

type webhookPayload struct {
	Event          string    `json:"event"`
	DeliveryID     int64     `json:"delivery_id"`
	AlertID        int64     `json:"alert_id"`
	RuleID         int64     `json:"rule_id"`
	RuleName       string    `json:"rule_name"`
	AccountID      int64     `json:"account_id"`
	PeerType       string    `json:"peer_type"`
	PeerID         int64     `json:"peer_id"`
	PeerTitle      string    `json:"peer_title"`
	MessageID      int       `json:"message_id"`
	MatchedKeyword string    `json:"matched_keyword,omitempty"`
	MessagePreview string    `json:"message_preview"`
	Severity       string    `json:"severity"`
	CreatedAt      time.Time `json:"created_at"`
}
