package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Key returns the SHA-256 of the Python-compatible 8-element JSON list.
func Key(baseURL, model, form, protectedSegment string) string {
	parts := []string{
		"1", "1", quote(baseURL), quote(model), `"en"`, `"ja"`, quote(form), quote(protectedSegment),
	}
	material := "[" + strings.Join(parts, ", ") + "]"
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func quote(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 {
				builder.WriteString(`\u00`)
				// r < 0x20 here, so the byte conversion is exact.
				builder.WriteByte(hexDigit(byte(r) >> 4))  //nolint:gosec // bounded control rune
				builder.WriteByte(hexDigit(byte(r) & 0xf)) //nolint:gosec // bounded control rune
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func hexDigit(value byte) byte {
	if value < 10 {
		return '0' + value
	}
	return 'a' + value - 10
}
