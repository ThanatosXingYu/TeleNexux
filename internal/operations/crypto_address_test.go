package operations

import (
	"strings"
	"testing"
)

func TestValidateCryptoAddressFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address string
		network string
		want    bool
	}{
		{
			name:    "EIP-55 checksum",
			address: "0x52908400098527886E0F7030069857D2E4169EE7",
			network: CryptoNetworkEthereum,
			want:    true,
		},
		{
			name:    "Bitcoin base58",
			address: "1BoatSLRHtKNngkdXEeobR76b53LETtpyT",
			network: CryptoNetworkBitcoin,
			want:    true,
		},
		{
			name:    "EVM uppercase prefix",
			address: "0X52908400098527886E0F7030069857D2E4169EE7",
			network: CryptoNetworkEthereum,
			want:    true,
		},
		{
			name:    "Bitcoin witness v0",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: CryptoNetworkBitcoin,
			want:    true,
		},
		{
			name:    "Bitcoin taproot",
			address: "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network: CryptoNetworkBitcoin,
			want:    true,
		},
		{
			name:    "TRON base58check",
			address: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
			network: CryptoNetworkTRON,
			want:    true,
		},
		{
			name:    "invalid EIP-55 letter case",
			address: "0x52908400098527886E0F7030069857D2E4169EE7A",
			network: CryptoNetworkEthereum,
			want:    false,
		},
		{
			name:    "invalid Bitcoin checksum",
			address: "1BoatSLRHtKNngkdXEeobR76b53LETtpyU",
			network: CryptoNetworkBitcoin,
			want:    false,
		},
		{
			name:    "invalid bech32 checksum",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt081",
			network: CryptoNetworkBitcoin,
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match, ok := validateCryptoAddress(test.address)
			if ok != test.want {
				t.Fatalf("validateCryptoAddress(%q) = %t, want %t", test.address, ok, test.want)
			}
			if ok && (match.Network != test.network || !strings.HasPrefix(match.ExplorerURL, "https://")) {
				t.Fatalf("address match = %+v", match)
			}
		})
	}
}

func TestExtractCryptoAddressesDeduplicatesNormalizedEVMText(t *testing.T) {
	t.Parallel()
	matches := extractCryptoAddresses(
		"first 0x52908400098527886E0F7030069857D2E4169EE7, " +
			"again 0x52908400098527886e0f7030069857d2e4169ee7",
	)
	if len(matches) != 1 || matches[0].Network != CryptoNetworkEthereum {
		t.Fatalf("crypto matches = %+v", matches)
	}
}
