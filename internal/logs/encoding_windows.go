//go:build windows

package logs

import (
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const windowsOEMCodePage = 1

func decodePlatformLogLine(line []byte) (string, bool) {
	if len(line) == 0 {
		return "", true
	}
	length, err := windows.MultiByteToWideChar(windowsOEMCodePage, 0, &line[0], int32(len(line)), nil, 0)
	if err != nil || length <= 0 {
		return "", false
	}
	wide := make([]uint16, length)
	if _, err := windows.MultiByteToWideChar(windowsOEMCodePage, 0, &line[0], int32(len(line)), &wide[0], length); err != nil {
		return "", false
	}
	decoded := string(utf16.Decode(wide))
	return decoded, utf8.ValidString(decoded)
}
