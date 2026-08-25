package logs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeLogLinePreservesUTF8AndRepairsInvalidInput(t *testing.T) {
	t.Parallel()

	if got := decodeLogLine([]byte("服务 ready")); got != "服务 ready" {
		t.Fatalf("decodeLogLine(valid UTF-8) = %q", got)
	}
	got := decodeLogLine([]byte{'b', 'a', 'd', 0xff, 'b', 'y', 't', 'e'})
	if !utf8.ValidString(got) || !strings.Contains(got, "bad") || !strings.Contains(got, "byte") {
		t.Fatalf("decodeLogLine(invalid bytes) = %q", got)
	}
}
