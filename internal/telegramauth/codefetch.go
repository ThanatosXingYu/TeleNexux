package telegramauth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const maxCodeResponse = 128 << 10

var (
	codeContextPattern = regexp.MustCompile(`(?is)(?:code|verification|sms|验证码|驗證碼)[^0-9]{0,80}([0-9]{4,8})`)
	codeJSONPattern    = regexp.MustCompile(`(?is)"(?:code|sms_code|verification_code)"\s*:\s*"?([0-9]{4,8})"?`)
	codeOnlyPattern    = regexp.MustCompile(`(?s)^\s*([0-9]{4,8})\s*$`)
	codeAnyPattern     = regexp.MustCompile(`\b([0-9]{4,8})\b`)
	htmlTagPattern     = regexp.MustCompile(`(?s)<[^>]+>`)
	blockedIPRanges    = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
)

type HTTPCodeFetcher struct {
	allowedHosts map[string]struct{}
	client       *http.Client
}

func NewHTTPCodeFetcher(hosts []string, timeout time.Duration) (*HTTPCodeFetcher, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	allowed := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, `/\:`) || net.ParseIP(host) != nil || host == "localhost" {
			return nil, fmt.Errorf("invalid code fetch host %q", host)
		}
		allowed[host] = struct{}{}
	}
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}
	dialer.Control = func(network, address string, _ syscall.RawConn) error {
		if !strings.HasPrefix(network, "tcp") {
			return fmt.Errorf("code fetch requires a TCP connection, got %q", network)
		}
		return validateDialAddress(address)
	}

	fetcher := &HTTPCodeFetcher{allowedHosts: allowed}
	fetcher.client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return fetcher.Validate(req.URL.String())
		},
	}
	return fetcher, nil
}

func (f *HTTPCodeFetcher) Validate(source string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return errors.New("URL must be an HTTPS address without credentials")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return errors.New("URL must use the default HTTPS port")
	}
	host := strings.ToLower(parsed.Hostname())
	if _, ok := f.allowedHosts[host]; !ok {
		return fmt.Errorf("host %q is not configured", host)
	}
	return nil
}

func validateDialAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse connection address: %w", err)
	}
	if port != "443" {
		return fmt.Errorf("code fetch must use HTTPS port 443, got %q", port)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("connection address did not resolve to an IP: %w", err)
	}
	ip = ip.Unmap()
	if !isPublicIP(ip) {
		return fmt.Errorf("code fetch resolved to a non-public address %s", ip)
	}
	return nil
}

func isPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() ||
		ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		!ip.IsGlobalUnicast() {
		return false
	}
	for _, blocked := range blockedIPRanges {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}

func (f *HTTPCodeFetcher) Fetch(
	ctx context.Context,
	source string,
	expectedLength int,
) (code string, err error) {
	if err := f.Validate(source); err != nil {
		return "", fmt.Errorf("%w: %v", ErrCodeSourceReject, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("create code request: %w", err)
	}
	request.Header.Set("Accept", "text/html, text/plain, application/json")
	request.Header.Set("User-Agent", "TeleNexus/0.1")

	response, err := f.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch verification code: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close verification code response: %w", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: source returned HTTP %d", ErrCodeNotReady, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxCodeResponse+1))
	if err != nil {
		return "", fmt.Errorf("read verification code response: %w", err)
	}
	if len(body) > maxCodeResponse {
		return "", fmt.Errorf("%w: response is too large", ErrCodeSourceReject)
	}
	code, err = extractCode(string(body), expectedLength)
	if err != nil {
		return "", err
	}
	return code, nil
}

func extractCode(raw string, expectedLength int) (string, error) {
	text := html.UnescapeString(htmlTagPattern.ReplaceAllString(raw, " "))
	patterns := []*regexp.Regexp{codeJSONPattern, codeContextPattern, codeOnlyPattern}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			code := match[1]
			if codeMatchesLength(code, expectedLength) {
				return code, nil
			}
		}
	}

	matches := codeAnyPattern.FindAllStringSubmatch(text, -1)
	var unique string
	for _, match := range matches {
		code := match[1]
		if !codeMatchesLength(code, expectedLength) {
			continue
		}
		if unique != "" && unique != code {
			return "", ErrCodeNotReady
		}
		unique = code
	}
	if unique == "" {
		return "", ErrCodeNotReady
	}
	return unique, nil
}

func codeMatchesLength(code string, expected int) bool {
	return expected <= 0 || len(code) == expected
}
