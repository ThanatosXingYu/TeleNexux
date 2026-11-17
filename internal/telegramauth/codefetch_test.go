package telegramauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestExtractCode(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		length  int
		want    string
		wantErr error
	}{
		{name: "JSON field", body: `{"code":"54321"}`, length: 5, want: "54321"},
		{name: "HTML context", body: `<p>Your verification code: <b>23456</b></p>`, length: 5, want: "23456"},
		{name: "HTML input", body: `<input id="code" type="text" value="54321" readonly>`, length: 5, want: "54321"},
		{name: "Chinese context", body: `<div>验证码 34567，请勿泄露</div>`, length: 5, want: "34567"},
		{name: "only code", body: " 45678 \n", length: 5, want: "45678"},
		{name: "not ready", body: "waiting for SMS", length: 5, wantErr: ErrCodeNotReady},
		{name: "ambiguous", body: "12345 and 67890", length: 5, wantErr: ErrCodeNotReady},
		{
			name:    "error page",
			body:    `<style>--color:#001529;--link:#123456cc</style><p>请求过于频繁，请等待 2秒再试。</p>`,
			length:  5,
			wantErr: ErrCodeNotReady,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, err := extractCode(tt.body, tt.length)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("extractCode() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || code != tt.want {
				t.Fatalf("extractCode() = %q, %v; want %q", code, err, tt.want)
			}
		})
	}
}

func TestHTTPCodeFetcherHostValidation(t *testing.T) {
	fetcher, err := NewHTTPCodeFetcher([]string{"tgok.cc"}, "1.1.1.1:53", 0)
	if err != nil {
		t.Fatalf("NewHTTPCodeFetcher() error = %v", err)
	}
	if err := fetcher.Validate("https://tgok.cc/path/GetHTML"); err != nil {
		t.Fatalf("allowed URL rejected: %v", err)
	}
	for _, source := range []string{
		"http://tgok.cc/path",
		"https://example.com/code",
		"https://user@tgok.cc/path",
		"https://tgok.cc:8443/path",
	} {
		if err := fetcher.Validate(source); err == nil {
			t.Errorf("Validate(%q) accepted an unsafe source", source)
		}
	}
}

func TestHTTPCodeFetcherRejectsPrivateConfiguredHost(t *testing.T) {
	if _, err := NewHTTPCodeFetcher([]string{"127.0.0.1"}, "1.1.1.1:53", 0); err == nil {
		t.Fatal("NewHTTPCodeFetcher() accepted a loopback host")
	}
}

func TestHTTPCodeFetcherRejectsInvalidDNSServer(t *testing.T) {
	for _, server := range []string{"", "dns.example.com:53", "1.1.1.1:5353"} {
		if _, err := NewHTTPCodeFetcher([]string{"tgok.cc"}, server, 0); err == nil {
			t.Errorf("NewHTTPCodeFetcher() accepted DNS server %q", server)
		}
	}
}

func TestHTTPCodeFetcherRedactsSourceFromRequestErrors(t *testing.T) {
	fetcher, err := NewHTTPCodeFetcher([]string{"tgok.cc"}, "1.1.1.1:53", 0)
	if err != nil {
		t.Fatalf("NewHTTPCodeFetcher() error = %v", err)
	}
	fetcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})

	_, err = fetcher.Fetch(
		context.Background(),
		"https://tgok.cc/private-token/GetHTML?secret=value",
		5,
	)
	if err == nil {
		t.Fatal("Fetch() error = nil, want request failure")
	}
	if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "secret=value") {
		t.Fatalf("Fetch() error leaked the source URL: %v", err)
	}
}

func TestValidateDialAddressRejectsNonPublicNetworks(t *testing.T) {
	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "public IPv4", address: "8.8.8.8:443"},
		{name: "public IPv6", address: "[2001:4860:4860::8888]:443"},
		{name: "loopback", address: "127.0.0.1:443", wantErr: true},
		{name: "private IPv4", address: "10.10.0.1:443", wantErr: true},
		{name: "carrier NAT", address: "100.64.0.1:443", wantErr: true},
		{name: "link local", address: "169.254.169.254:443", wantErr: true},
		{name: "documentation", address: "192.0.2.1:443", wantErr: true},
		{name: "documentation IPv6", address: "[2001:db8::1]:443", wantErr: true},
		{name: "wrong port", address: "8.8.8.8:8443", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDialAddress(tt.address)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDialAddress(%q) error = %v, wantErr %v", tt.address, err, tt.wantErr)
			}
		})
	}
}
