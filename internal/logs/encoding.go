package logs

import (
	"strings"
	"unicode/utf8"
)

func decodeLogLine(line []byte) string {
	if utf8.Valid(line) {
		return string(line)
	}
	if decoded, ok := decodePlatformLogLine(line); ok {
		return decoded
	}
	return strings.ToValidUTF8(string(line), "\uFFFD")
}
