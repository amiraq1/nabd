package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

const maxStrictJSONBytes = 32 << 10

// DecodeStrictJSONObject decodes raw into dst while rejecting invalid UTF-8,
// non-object payloads, duplicate keys at any object level, unknown fields, and
// trailing JSON tokens.
func DecodeStrictJSONObject(raw []byte, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: empty JSON input", ErrInvalidInput)
	}
	if len(raw) > maxStrictJSONBytes {
		return fmt.Errorf("%w: JSON arguments exceed %d bytes", ErrInvalidInput, maxStrictJSONBytes)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("%w: JSON input must be valid UTF-8", ErrInvalidInput)
	}
	if err := validateNoDuplicateJSONKeys(trimmed); err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: unexpected trailing JSON data", ErrInvalidInput)
	}
	return nil
}

func validateNoDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrInvalidInput, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("%w: expected a JSON object", ErrInvalidInput)
	}
	if err := validateJSONObjectTokens(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: unexpected trailing JSON data", ErrInvalidInput)
	}
	return nil
}

func validateJSONObjectTokens(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: invalid JSON key: %v", ErrInvalidInput, err)
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("%w: expected string object key", ErrInvalidInput)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate JSON key %q", ErrInvalidInput, key)
		}
		seen[key] = struct{}{}
		if err := validateJSONValueTokens(dec); err != nil {
			return err
		}
	}
	endTok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON object termination: %v", ErrInvalidInput, err)
	}
	if d, ok := endTok.(json.Delim); !ok || d != '}' {
		return fmt.Errorf("%w: expected closing '}'", ErrInvalidInput)
	}
	return nil
}

func validateJSONValueTokens(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON value: %v", ErrInvalidInput, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return validateJSONObjectTokens(dec)
	case '[':
		for dec.More() {
			if err := validateJSONValueTokens(dec); err != nil {
				return err
			}
		}
		endTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: invalid JSON array termination: %v", ErrInvalidInput, err)
		}
		if d, ok := endTok.(json.Delim); !ok || d != ']' {
			return fmt.Errorf("%w: expected closing ']'", ErrInvalidInput)
		}
		return nil
	default:
		return fmt.Errorf("%w: unexpected JSON delimiter %q", ErrInvalidInput, delim)
	}
}
