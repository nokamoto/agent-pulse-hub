package pulse

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxFrameBytes       = 64 * 1024
	MaxContextBytes     = 8 * 1024
	MaxRegistrations    = 1024
	MaxDeliveryInFlight = 128
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var (
	ErrInvalidUUID     = errors.New("invalid UUID")
	ErrInvalidWatchArg = errors.New("watch_args must be a JSON object")
)

type WatchRequest struct {
	RequestID      string          `json:"request_id"`
	SubscriptionID string          `json:"subscription_id"`
	WatchArgs      json.RawMessage `json:"watch_args"`
}

type Event struct {
	SubscriptionID string
	Context        string
}

type Delivery struct {
	ID             string
	Plugin         string
	SubscriptionID string
	SessionID      string
	Context        string
}

type DeliveryOutcome string

const (
	DeliveryAccepted DeliveryOutcome = "accepted"
	DeliveryFailed   DeliveryOutcome = "failed"
	DeliveryUnknown  DeliveryOutcome = "unknown"
)

func ValidateUUID(value string) error {
	if !uuidPattern.MatchString(value) {
		return fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}
	return nil
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded), nil
}

// CanonicalWatchArgs validates a JSON object and returns a stable encoding.
// Object keys are sorted, array order is retained, and decimal numbers are
// normalized without converting through floating point.
func CanonicalWatchArgs(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return "", fmt.Errorf("decode watch_args: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return "", ErrInvalidWatchArg
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("watch_args contains trailing JSON")
		}
		return "", fmt.Errorf("decode trailing watch_args data: %w", err)
	}
	return canonicalValue(value)
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				value, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			var array []any
			for decoder.More() {
				value, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			_, err := decoder.Token()
			return array, err
		default:
			return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
		}
	}
	return token, nil
}

func canonicalValue(value any) (string, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var builder strings.Builder
		builder.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				builder.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			builder.Write(encodedKey)
			builder.WriteByte(':')
			encodedValue, err := canonicalValue(typed[key])
			if err != nil {
				return "", err
			}
			builder.WriteString(encodedValue)
		}
		builder.WriteByte('}')
		return builder.String(), nil
	case []any:
		var builder strings.Builder
		builder.WriteByte('[')
		for index, item := range typed {
			if index != 0 {
				builder.WriteByte(',')
			}
			encoded, err := canonicalValue(item)
			if err != nil {
				return "", err
			}
			builder.WriteString(encoded)
		}
		builder.WriteByte(']')
		return builder.String(), nil
	case json.Number:
		return canonicalNumber(typed.String())
	default:
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	}
}

func canonicalNumber(number string) (string, error) {
	negative := strings.HasPrefix(number, "-")
	if negative {
		number = number[1:]
	}
	exponent := new(big.Int)
	if index := strings.IndexAny(number, "eE"); index >= 0 {
		if _, ok := exponent.SetString(number[index+1:], 10); !ok {
			return "", fmt.Errorf("invalid JSON number exponent %q", number)
		}
		number = number[:index]
	}
	if point := strings.IndexByte(number, '.'); point >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(number)-point-1)))
		number = number[:point] + number[point+1:]
	}
	digits := strings.TrimLeft(number, "0")
	if digits == "" {
		return "0", nil
	}
	trailingZeros := len(digits) - len(strings.TrimRight(digits, "0"))
	if trailingZeros != 0 {
		digits = digits[:len(digits)-trailingZeros]
		exponent.Add(exponent, big.NewInt(int64(trailingZeros)))
	}
	if negative {
		digits = "-" + digits
	}
	return digits + "e" + exponent.String(), nil
}

func ValidateEvent(event Event) error {
	if event.SubscriptionID == "" {
		return errors.New("subscription_id is empty")
	}
	if event.Context == "" {
		return errors.New("context is empty")
	}
	if !utf8.ValidString(event.Context) {
		return errors.New("context is not valid UTF-8")
	}
	if len([]byte(event.Context)) > MaxContextBytes {
		return fmt.Errorf("context exceeds %d UTF-8 bytes", MaxContextBytes)
	}
	return nil
}
