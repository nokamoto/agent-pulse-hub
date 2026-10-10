package jsonvalue

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind identifies a JSON value kind.
type Kind uint8

const (
	Null Kind = iota
	Boolean
	Number
	String
	Array
	Object
)

// Value is an immutable JSON value. Number tokens are retained exactly.
type Value struct {
	kind   Kind
	bool   bool
	text   string
	array  []*Value
	object map[string]*Value
}

// Parse strictly parses one JSON value. It rejects invalid UTF-8, duplicate
// object keys, and unpaired UTF-16 surrogate escapes.
func Parse(data []byte, maxBytes int) (*Value, error) {
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, fmt.Errorf("JSON input exceeds %d bytes", maxBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("JSON input is not valid UTF-8")
	}
	p := parser{data: data}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, p.error("unexpected trailing data")
	}
	return v, nil
}

// NewObject constructs an object and copies its field map.
func NewObject(fields map[string]*Value) *Value {
	copyFields := make(map[string]*Value, len(fields))
	for key, value := range fields {
		copyFields[key] = value
	}
	return &Value{kind: Object, object: copyFields}
}

// NewArray constructs an array and copies its values.
func NewArray(values ...*Value) *Value {
	return &Value{kind: Array, array: append([]*Value(nil), values...)}
}

func NewString(value string) *Value { return &Value{kind: String, text: value} }
func NewNumber(token string) *Value { return &Value{kind: Number, text: token} }
func NewBoolean(value bool) *Value  { return &Value{kind: Boolean, bool: value} }
func NewNull() *Value               { return &Value{kind: Null} }

func (v *Value) Kind() Kind {
	if v == nil {
		return Null
	}
	return v.kind
}

func (v *Value) Bool() (bool, bool) {
	if v == nil || v.kind != Boolean {
		return false, false
	}
	return v.bool, true
}

func (v *Value) Text() (string, bool) {
	if v == nil || (v.kind != String && v.kind != Number) {
		return "", false
	}
	return v.text, true
}

func (v *Value) Get(key string) (*Value, bool) {
	if v == nil || v.kind != Object {
		return nil, false
	}
	value, ok := v.object[key]
	return value, ok
}

func (v *Value) Fields() map[string]*Value {
	if v == nil || v.kind != Object {
		return nil
	}
	fields := make(map[string]*Value, len(v.object))
	for key, value := range v.object {
		fields[key] = value
	}
	return fields
}

func (v *Value) Items() []*Value {
	if v == nil || v.kind != Array {
		return nil
	}
	return append([]*Value(nil), v.array...)
}

// Marshal returns compact JSON with object keys in lexical order. Number
// spellings are preserved, so no conversion through binary floating point
// occurs when a watch request is forwarded.
func (v *Value) Marshal() []byte {
	var out []byte
	return appendJSON(out, v, false)
}

// Canonical returns a stable representation used to compare watch arguments.
// Object order is ignored, array order is preserved, and mathematically equal
// decimal number tokens have the same representation.
func (v *Value) Canonical() []byte {
	var out []byte
	return appendJSON(out, v, true)
}

func (v *Value) Equivalent(other *Value) bool {
	return bytes.Equal(v.Canonical(), other.Canonical())
}

func appendJSON(dst []byte, v *Value, canonicalNumbers bool) []byte {
	if v == nil {
		return append(dst, "null"...)
	}
	switch v.kind {
	case Null:
		return append(dst, "null"...)
	case Boolean:
		if v.bool {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case Number:
		if canonicalNumbers {
			return append(dst, canonicalNumber(v.text)...)
		}
		return append(dst, v.text...)
	case String:
		return appendJSONString(dst, v.text)
	case Array:
		dst = append(dst, '[')
		for i, item := range v.array {
			if i != 0 {
				dst = append(dst, ',')
			}
			dst = appendJSON(dst, item, canonicalNumbers)
		}
		return append(dst, ']')
	case Object:
		keys := make([]string, 0, len(v.object))
		for key := range v.object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dst = append(dst, '{')
		for i, key := range keys {
			if i != 0 {
				dst = append(dst, ',')
			}
			dst = appendJSONString(dst, key)
			dst = append(dst, ':')
			dst = appendJSON(dst, v.object[key], canonicalNumbers)
		}
		return append(dst, '}')
	default:
		return append(dst, "null"...)
	}
}

func appendJSONString(dst []byte, value string) []byte {
	dst = append(dst, '"')
	for _, r := range value {
		switch r {
		case '"':
			dst = append(dst, `\"`...)
		case '\\':
			dst = append(dst, `\\`...)
		case '\b':
			dst = append(dst, `\b`...)
		case '\f':
			dst = append(dst, `\f`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\r':
			dst = append(dst, `\r`...)
		case '\t':
			dst = append(dst, `\t`...)
		default:
			if r < 0x20 {
				dst = append(dst, `\u00`...)
				dst = append(dst, "0123456789abcdef"[byte(r)>>4])
				dst = append(dst, "0123456789abcdef"[byte(r)&0xf])
			} else {
				dst = utf8.AppendRune(dst, r)
			}
		}
	}
	return append(dst, '"')
}

func canonicalNumber(token string) string {
	negative := strings.HasPrefix(token, "-")
	unsigned := strings.TrimPrefix(token, "-")
	exponent := new(big.Int)
	if index := strings.IndexAny(unsigned, "eE"); index >= 0 {
		exponent.SetString(unsigned[index+1:], 10)
		unsigned = unsigned[:index]
	}
	fracDigits := 0
	if index := strings.IndexByte(unsigned, '.'); index >= 0 {
		fracDigits = len(unsigned) - index - 1
		unsigned = unsigned[:index] + unsigned[index+1:]
	}
	digits := strings.TrimLeft(unsigned, "0")
	if digits == "" {
		return "0"
	}
	exponent.Sub(exponent, big.NewInt(int64(fracDigits)))
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmed))))
	if negative {
		digits = "-" + trimmed
	} else {
		digits = trimmed
	}
	return digits + "e" + exponent.String()
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) error(message string) error {
	return fmt.Errorf("JSON byte %d: %s", p.pos, message)
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value() (*Value, error) {
	if p.pos >= len(p.data) {
		return nil, p.error("expected a value")
	}
	switch p.data[p.pos] {
	case 'n':
		if p.literal("null") {
			return NewNull(), nil
		}
	case 't':
		if p.literal("true") {
			return NewBoolean(true), nil
		}
	case 'f':
		if p.literal("false") {
			return NewBoolean(false), nil
		}
	case '"':
		text, err := p.string()
		if err != nil {
			return nil, err
		}
		return NewString(text), nil
	case '[':
		return p.array()
	case '{':
		return p.object()
	default:
		if p.data[p.pos] == '-' || (p.data[p.pos] >= '0' && p.data[p.pos] <= '9') {
			return p.number()
		}
	}
	return nil, p.error("invalid value")
}

func (p *parser) literal(value string) bool {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(value)) {
		return false
	}
	p.pos += len(value)
	return true
}

func (p *parser) string() (string, error) {
	p.pos++ // opening quote
	var out strings.Builder
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		switch b {
		case '"':
			return out.String(), nil
		case '\\':
			if p.pos >= len(p.data) {
				return "", p.error("incomplete string escape")
			}
			escaped := p.data[p.pos]
			p.pos++
			switch escaped {
			case '"', '\\', '/':
				out.WriteByte(escaped)
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'u':
				r, err := p.unicodeEscape()
				if err != nil {
					return "", err
				}
				out.WriteRune(r)
			default:
				return "", p.error("invalid string escape")
			}
		default:
			if b < 0x20 {
				return "", p.error("unescaped control character in string")
			}
			if b < utf8.RuneSelf {
				out.WriteByte(b)
				continue
			}
			p.pos--
			r, size := utf8.DecodeRune(p.data[p.pos:])
			if r == utf8.RuneError && size == 1 {
				return "", p.error("invalid UTF-8 in string")
			}
			out.WriteRune(r)
			p.pos += size
		}
	}
	return "", p.error("unterminated string")
}

func (p *parser) unicodeEscape() (rune, error) {
	first, err := p.hex4()
	if err != nil {
		return 0, err
	}
	if first >= 0xdc00 && first <= 0xdfff {
		return 0, p.error("unpaired low surrogate")
	}
	if first < 0xd800 || first > 0xdbff {
		return rune(first), nil
	}
	if p.pos+2 > len(p.data) || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
		return 0, p.error("unpaired high surrogate")
	}
	p.pos += 2
	second, err := p.hex4()
	if err != nil {
		return 0, err
	}
	if second < 0xdc00 || second > 0xdfff {
		return 0, p.error("invalid surrogate pair")
	}
	return 0x10000 + rune(first-0xd800)*0x400 + rune(second-0xdc00), nil
}

func (p *parser) hex4() (uint16, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.error("incomplete Unicode escape")
	}
	var value uint16
	for i := 0; i < 4; i++ {
		b := p.data[p.pos+i]
		value <<= 4
		switch {
		case b >= '0' && b <= '9':
			value |= uint16(b - '0')
		case b >= 'a' && b <= 'f':
			value |= uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			value |= uint16(b-'A') + 10
		default:
			return 0, p.error("invalid Unicode escape")
		}
	}
	p.pos += 4
	return value, nil
}

func (p *parser) array() (*Value, error) {
	p.pos++
	p.space()
	values := make([]*Value, 0)
	if p.consume(']') {
		return NewArray(), nil
	}
	for {
		p.space()
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		p.space()
		if p.consume(']') {
			return NewArray(values...), nil
		}
		if !p.consume(',') {
			return nil, p.error("expected comma or closing bracket")
		}
	}
}

func (p *parser) object() (*Value, error) {
	p.pos++
	p.space()
	fields := make(map[string]*Value)
	if p.consume('}') {
		return NewObject(fields), nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.error("expected object key")
		}
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("JSON byte %d: duplicate object key %q", p.pos, key)
		}
		p.space()
		if !p.consume(':') {
			return nil, p.error("expected colon")
		}
		p.space()
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		fields[key] = value
		p.space()
		if p.consume('}') {
			return NewObject(fields), nil
		}
		if !p.consume(',') {
			return nil, p.error("expected comma or closing brace")
		}
	}
}

func (p *parser) number() (*Value, error) {
	start := p.pos
	if p.consume('-') && p.pos >= len(p.data) {
		return nil, p.error("incomplete number")
	}
	if p.consume('0') {
		if p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			return nil, p.error("leading zero in number")
		}
	} else {
		if p.pos >= len(p.data) || p.data[p.pos] < '1' || p.data[p.pos] > '9' {
			return nil, p.error("invalid integer part")
		}
		for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			p.pos++
		}
	}
	if p.consume('.') {
		if p.pos >= len(p.data) || !isDigit(p.data[p.pos]) {
			return nil, p.error("fraction requires a digit")
		}
		for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			p.pos++
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if p.pos >= len(p.data) || !isDigit(p.data[p.pos]) {
			return nil, p.error("exponent requires a digit")
		}
		for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			p.pos++
		}
	}
	return NewNumber(string(p.data[start:p.pos])), nil
}

func (p *parser) consume(expected byte) bool {
	if p.pos >= len(p.data) || p.data[p.pos] != expected {
		return false
	}
	p.pos++
	return true
}

func isDigit(value byte) bool { return value >= '0' && value <= '9' }

// ParseString returns a JSON string value after strict validation.
func ParseString(value *Value) (string, bool) {
	if value == nil || value.kind != String {
		return "", false
	}
	return value.text, true
}

// ParseInt returns an integer only when the JSON number is an integer token
// within the platform int range.
func ParseInt(value *Value) (int, bool) {
	if value == nil || value.kind != Number {
		return 0, false
	}
	parsed, err := strconv.Atoi(value.text)
	return parsed, err == nil
}
