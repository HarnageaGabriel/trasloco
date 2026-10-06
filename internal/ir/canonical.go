package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonical returns RFC 8785 JSON Canonicalization Scheme bytes for data that
// encoding/json can emit. It rejects invalid UTF-8, non-finite numbers, cycles,
// and JSON outside the I-JSON data model through encoding/json.
func Canonical(value any) ([]byte, error) {
	if err := validText(reflect.ValueOf(value), make(map[visit]bool)); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical JSON input: %w", err)
	}
	return CanonicalJSON(encoded)
}

// CanonicalJSON canonicalizes one JSON value. Numbers are interpreted as
// IEEE-754 binary64, the RFC 8785 interoperability domain.
func CanonicalJSON(data []byte) ([]byte, error) {
	if err := uniqueObjectNames(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode canonical JSON input: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	var result bytes.Buffer
	if err := writeCanonical(&result, value); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

func uniqueObjectNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("validate canonical JSON input: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("validate canonical JSON input: multiple JSON values")
		}
		return fmt.Errorf("validate canonical JSON input: %w", err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is %T, want string", keyToken)
			}
			if seen[key] {
				return fmt.Errorf("duplicate object name %q", key)
			}
			seen[key] = true
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("object ended with %v", end)
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("array ended with %v", end)
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}

// Digest returns the schema-specified SHA-256 digest of canonical JSON.
func Digest(value any) (string, error) {
	encoded, err := Canonical(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		if !utf8.ValidString(typed) {
			return fmt.Errorf("canonical JSON string is not valid UTF-8")
		}
		writeString(out, typed)
	case json.Number:
		floating, err := typed.Float64()
		if err != nil {
			return fmt.Errorf("canonical JSON number %q is outside binary64: %w", typed, err)
		}
		if floating == 0 {
			out.WriteByte('0')
			return nil
		}
		encoded, err := json.Marshal(floating)
		if err != nil {
			return fmt.Errorf("canonical JSON number %q: %w", typed, err)
		}
		out.Write(encoded)
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			writeString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func writeString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	start := 0
	for index, r := range value {
		var escape string
		switch r {
		case '\b':
			escape = `\b`
		case '\t':
			escape = `\t`
		case '\n':
			escape = `\n`
		case '\f':
			escape = `\f`
		case '\r':
			escape = `\r`
		case '"':
			escape = `\"`
		case '\\':
			escape = `\\`
		default:
			if r < 0x20 {
				escape = fmt.Sprintf(`\u%04x`, r)
			}
		}
		if escape == "" {
			continue
		}
		out.WriteString(value[start:index])
		out.WriteString(escape)
		start = index + utf8.RuneLen(r)
	}
	out.WriteString(value[start:])
	out.WriteByte('"')
}

func utf16Less(left, right string) bool {
	l := utf16.Encode([]rune(left))
	r := utf16.Encode([]rune(right))
	for index := 0; index < len(l) && index < len(r); index++ {
		if l[index] != r[index] {
			return l[index] < r[index]
		}
	}
	return len(l) < len(r)
}

type visit struct {
	typeOf reflect.Type
	ptr    uintptr
}

func validText(value reflect.Value, seen map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.String {
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("canonical JSON input contains invalid UTF-8")
		}
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		key := visit{typeOf: value.Type(), ptr: value.Pointer()}
		if seen[key] {
			return fmt.Errorf("canonical JSON input contains a cycle")
		}
		seen[key] = true
		defer delete(seen, key)
		return validText(value.Elem(), seen)
	}
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath == "" {
				if err := validText(value.Field(index), seen); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validText(iterator.Key(), seen); err != nil {
				return err
			}
			if err := validText(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		fallthrough
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validText(value.Index(index), seen); err != nil {
				return err
			}
		}
	}
	return nil
}
