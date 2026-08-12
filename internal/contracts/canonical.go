package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// MarshalCanonical emits UTF-8 RFC 8785 JSON. V1 domain numbers are integers;
// fractional quantities are fixed-scale Decimal strings.
func MarshalCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, tree); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func CanonicalSHA256(value any) (string, error) {
	b, err := MarshalCanonical(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func writeCanonical(out *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if x {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		writeJSONString(out, x)
	case json.Number:
		s := x.String()
		if bytes.ContainsAny([]byte(s), ".eE") {
			return errors.New("floating-point JSON number forbidden")
		}
		if strings.HasPrefix(s, "-") {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < -9007199254740991 {
				return fmt.Errorf("integer outside RFC 8785 safe range %q", s)
			}
		} else {
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil || n > 9007199254740991 {
				return fmt.Errorf("integer outside RFC 8785 safe range %q", s)
			}
		}
		out.WriteString(s)
	case []any:
		out.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, k)
			out.WriteByte(':')
			if err := writeCanonical(out, x[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON type %T", v)
	}
	return nil
}
func writeJSONString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
func utf16Less(a, b string) bool {
	aa, bb := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return len(aa) < len(bb)
}
