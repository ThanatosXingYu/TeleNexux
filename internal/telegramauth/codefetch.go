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
	codeJSONPattern  = regexp.MustCompile(`(?is)"(?:code|sms_code|verification_code)"\s*:\s*"?([0-9]{4,8})"?`)
	codeInputIDFirst = regexp.MustCompile(
		`(?is)<input\b[^>]*\bid\s*=\s*["']code["'][^>]*\bvalue\s*=\s*["']([0-9]{4,8})["'][^>]*>`,
	)
	codeInputValueFirst = regexp.MustCompile(
		`(?is)<input\b[^>]*\bvalue\s*=\s*["']([0-9]{4,8})["'][^>]*\bid\s*=\s*["']code["'][^>]*>`,
	)
	codeLabelPattern = regexp.MustCompile(
		`(?is)(?:登录验证码|设备验证码|验证码|驗證碼|verification\s+code|login\s+code)\s*[:：]?(?:\s|<[^>]*>){0,12}([0-9]{4,8})`,
	)
	codeOnlyPattern = regexp.MustCompile(`(?s)^\s*([0-9]{4,8})\s*$`)
	htmlTagPattern  = regexp.MustCompile(`(?s)<[^>]+>`)
	blockedIPRanges = []netip.Prefix{
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
	resolver     *net.Resolver
	dialer       *net.Dialer
	client       *http.Client
}

func NewHTTPCodeFetcher(
	hosts []string,
	dnsServer string,
	timeout time.Duration,
) (*HTTPCodeFetcher, error) {
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

	dnsServer = strings.TrimSpace(dnsServer)
	if dnsServer == "" {
		return nil, errors.New("code fetch DNS server must not be empty")
	}
	if err := validateDNSServer(dnsServer); err != nil {
		return nil, err
	}

	resolverDialer := &net.Dialer{Timeout: timeout}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return resolverDialer.DialContext(ctx, network, dnsServer)
		},
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

	fetcher := &HTTPCodeFetcher{
		allowedHosts: allowed,
		resolver:     resolver,
		dialer:       dialer,
	}
	fetcher.client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           fetcher.dialContext,
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

func (f *HTTPCodeFetcher) dialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse code fetch address: %w", err)
	}
	if port != "443" {
		return nil, fmt.Errorf("code fetch must use HTTPS port 443, got %q", port)
	}

	addresses, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve code fetch host %q: %w", host, err)
	}
	var lastErr error
	for _, ip := range addresses {
		ip = ip.Unmap()
		if !isPublicIP(ip) {
			continue
		}
		target := net.JoinHostPort(ip.String(), port)
		connection, err := f.dialer.DialContext(ctx, network, target)
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("connect to code fetch host %q: %w", host, lastErr)
	}
	return nil, fmt.Errorf("code fetch host %q resolved only to non-public addresses", host)
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
		return "", sanitizeRequestError(err)
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

func sanitizeRequestError(err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) && requestErr.Err != nil {
		return fmt.Errorf("fetch verification code request: %w", requestErr.Err)
	}
	return errors.New("fetch verification code request failed")
}

func validateDNSServer(server string) error {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return fmt.Errorf("parse code fetch DNS server: %w", err)
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return fmt.Errorf("code fetch DNS server must be an IP address: %w", err)
	}
	if port != "53" {
		return fmt.Errorf("code fetch DNS server must use port 53, got %q", port)
	}
	return nil
}

func extractCode(raw string, expectedLength int) (string, error) {
	structuredPatterns := []*regexp.Regexp{
		codeJSONPattern,
		codeInputIDFirst,
		codeInputValueFirst,
	}
	for _, pattern := range structuredPatterns {
		for _, match := range pattern.FindAllStringSubmatch(raw, -1) {
			code := match[1]
			if codeMatchesLength(code, expectedLength) {
				return code, nil
			}
		}
	}

	text := html.UnescapeString(htmlTagPattern.ReplaceAllString(raw, " "))
	textPatterns := []*regexp.Regexp{
		codeLabelPattern,
		codeOnlyPattern,
	}
	for _, pattern := range textPatterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			code := match[1]
			if codeMatchesLength(code, expectedLength) {
				return code, nil
			}
		}
	}
	return "", ErrCodeNotReady
}

func codeMatchesLength(code string, expected int) bool {
	return expected <= 0 || len(code) == expected
}
