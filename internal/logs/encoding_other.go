//go:build !windows

package logs

func decodePlatformLogLine([]byte) (string, bool) {
	return "", false
}
