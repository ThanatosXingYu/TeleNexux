package operations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/sha3"
)

const (
	CryptoNetworkEthereum = "ethereum"
	CryptoNetworkTRON     = "tron"
	CryptoNetworkBitcoin  = "bitcoin"
)

type cryptoAddressMatch struct {
	Address      string
	Network      string
	NetworkLabel string
	ExplorerURL  string
}

func extractCryptoAddresses(text string) []cryptoAddressMatch {
	matches := make([]cryptoAddressMatch, 0, 2)
	seen := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(text, isCryptoAddressDelimiter) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		match, ok := validateCryptoAddress(token)
		if !ok {
			continue
		}
		key := cryptoAddressKey(match.Network, match.Address)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		matches = append(matches, match)
	}
	return matches
}

func isCryptoAddressDelimiter(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n', '<', '>', '"', '\'', '`',
		',', ';', ':', '!', '?', '(', ')', '[', ']', '{', '}', '，',
		'。', '；', '：', '！', '？', '（', '）', '【', '】':
		return true
	default:
		return false
	}
}

func validateCryptoAddress(raw string) (cryptoAddressMatch, bool) {
	address := strings.TrimSpace(raw)
	if address == "" || len(address) > 96 {
		return cryptoAddressMatch{}, false
	}
	evmAddress := strings.TrimPrefix(strings.TrimPrefix(address, "0x"), "0X")
	if len(address) == 42 && len(evmAddress) == 40 {
		if !isHexString(evmAddress) || !validEVMChecksum(evmAddress) {
			return cryptoAddressMatch{}, false
		}
		return newCryptoAddressMatch(
			address,
			CryptoNetworkEthereum,
			"Ethereum / EVM",
			"https://etherscan.io/address/"+address,
		), true
	}
	if len(address) == 34 && address[0] == 'T' {
		payload, ok := decodeBase58Check(address)
		if !ok || len(payload) != 21 || payload[0] != 0x41 {
			return cryptoAddressMatch{}, false
		}
		return newCryptoAddressMatch(
			address,
			CryptoNetworkTRON,
			"TRON",
			"https://tronscan.org/#/address/"+address,
		), true
	}
	if len(address) >= 26 && len(address) <= 35 && (address[0] == '1' || address[0] == '3') {
		payload, ok := decodeBase58Check(address)
		if !ok || len(payload) != 21 || (payload[0] != 0x00 && payload[0] != 0x05) {
			return cryptoAddressMatch{}, false
		}
		return newCryptoAddressMatch(
			address,
			CryptoNetworkBitcoin,
			"Bitcoin",
			"https://mempool.space/address/"+address,
		), true
	}
	lower := strings.ToLower(address)
	if strings.HasPrefix(lower, "bc1") {
		if !validBech32BitcoinAddress(lower) {
			return cryptoAddressMatch{}, false
		}
		return newCryptoAddressMatch(
			address,
			CryptoNetworkBitcoin,
			"Bitcoin",
			"https://mempool.space/address/"+address,
		), true
	}
	return cryptoAddressMatch{}, false
}

func newCryptoAddressMatch(
	address string,
	network string,
	networkLabel string,
	explorerURL string,
) cryptoAddressMatch {
	return cryptoAddressMatch{
		Address:      address,
		Network:      network,
		NetworkLabel: networkLabel,
		ExplorerURL:  explorerURL,
	}
}

func isHexString(value string) bool {
	if len(value) == 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Mixed-case EVM addresses encode an EIP-55 checksum. All-lowercase and
// all-uppercase forms remain valid because their checksum is not encoded.
func validEVMChecksum(body string) bool {
	if body == strings.ToLower(body) || body == strings.ToUpper(body) {
		return true
	}
	digest := sha3.NewLegacyKeccak256()
	_, _ = digest.Write([]byte(strings.ToLower(body)))
	hash := digest.Sum(nil)
	for index, value := range body {
		nibble := hash[index/2]
		if index%2 == 0 {
			nibble >>= 4
		} else {
			nibble &= 0x0f
		}
		if value >= 'a' && value <= 'f' && nibble >= 8 {
			return false
		}
		if value >= 'A' && value <= 'F' && nibble < 8 {
			return false
		}
	}
	return true
}

func decodeBase58Check(value string) ([]byte, bool) {
	raw, ok := decodeBase58(value)
	if !ok || len(raw) < 5 {
		return nil, false
	}
	payload := raw[:len(raw)-4]
	checksum := sha256.Sum256(payload)
	checksum = sha256.Sum256(checksum[:])
	return payload, bytes.Equal(raw[len(raw)-4:], checksum[:4])
}

func decodeBase58(value string) ([]byte, bool) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	result := make([]byte, 0, len(value))
	for _, character := range value {
		digit := strings.IndexRune(alphabet, character)
		if digit < 0 {
			return nil, false
		}
		carry := digit
		for index := len(result) - 1; index >= 0; index-- {
			carry += int(result[index]) * 58
			result[index] = byte(carry)
			carry >>= 8
		}
		for carry > 0 {
			result = append([]byte{byte(carry)}, result...)
			carry >>= 8
		}
	}
	for len(value) > 0 && value[0] == '1' {
		result = append([]byte{0}, result...)
		value = value[1:]
	}
	return result, len(result) > 0
}

func validBech32BitcoinAddress(value string) bool {
	separator := strings.LastIndexByte(value, '1')
	if separator < 1 || separator+7 > len(value) || len(value)-separator-1 > 90 {
		return false
	}
	hrp := value[:separator]
	if hrp != "bc" {
		return false
	}
	dataPart := value[separator+1:]
	values := make([]byte, len(dataPart))
	for index, character := range dataPart {
		digit := strings.IndexRune("qpzry9x8gf2tvdw0s3jn54khce6mua7l", character)
		if digit < 0 {
			return false
		}
		values[index] = byte(digit)
	}
	payload := values[:len(values)-6]
	if len(payload) == 0 {
		return false
	}
	version := payload[0]
	if version > 16 || !verifyBech32Checksum(hrp, values, version) {
		return false
	}
	converted, ok := convertBits(payload[1:], 5, 8, false)
	if !ok || version == 0 && len(converted) != 20 && len(converted) != 32 ||
		version > 0 && (len(converted) < 2 || len(converted) > 40) {
		return false
	}
	return true
}

func verifyBech32Checksum(hrp string, values []byte, version byte) bool {
	const modulus = 0x2bc830a3
	expanded := make([]byte, 0, len(hrp)*2+1+len(values))
	for _, character := range hrp {
		expanded = append(expanded, byte(character>>5))
	}
	expanded = append(expanded, 0)
	for _, character := range hrp {
		expanded = append(expanded, byte(character&31))
	}
	expanded = append(expanded, values...)
	checksum := 1
	for _, value := range expanded {
		top := checksum >> 25
		checksum = (checksum&0x1ffffff)<<5 ^ int(value)
		for index, generator := range []int{
			0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3,
		} {
			if (top>>index)&1 != 0 {
				checksum ^= generator
			}
		}
	}
	if version == 0 {
		return checksum == 1
	}
	return checksum == modulus
}

func convertBits(data []byte, fromBits uint, toBits uint, pad bool) ([]byte, bool) {
	var accumulator uint
	var bits uint
	maxValue := uint(1<<toBits) - 1
	maxAccumulator := uint(1<<(fromBits+toBits-1)) - 1
	result := make([]byte, 0, len(data)*int(fromBits)/int(toBits)+1)
	for _, value := range data {
		if uint(value)>>fromBits != 0 {
			return nil, false
		}
		accumulator = (accumulator<<fromBits | uint(value)) & maxAccumulator
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte(accumulator>>bits&maxValue))
		}
	}
	if pad {
		if bits > 0 {
			result = append(result, byte(accumulator<<(toBits-bits)&maxValue))
		}
	} else if bits >= fromBits || accumulator<<(toBits-bits)&maxValue != 0 {
		return nil, false
	}
	return result, true
}

func cryptoAddressKey(network, address string) string {
	if network == CryptoNetworkEthereum {
		address = strings.ToLower(address)
	}
	return network + "\x00" + address
}
