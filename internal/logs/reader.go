package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

func readSpool(ctx context.Context, file *os.File, stream Stream, initialOffset int64, maxLine int, poll time.Duration, output chan<- rawRecord) {
	buffer := make([]byte, 32*1024)
	pending := make([]byte, 0, maxLine+1)
	pendingStart := initialOffset
	continued := false
	for {
		count, err := file.Read(buffer)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			pending, continued, pendingStart = emitCompleteLines(output, stream, pending, continued, maxLine, pendingStart)
		}
		if err != nil && !errorsIsEOF(err) {
			output <- rawRecord{stream: stream, err: fmt.Errorf("read %s spool: %w", stream, err)}
			return
		}
		if ctx.Err() != nil {
			if len(pending) > 0 {
				emitLine(output, stream, pending, continued, pendingStart+int64(len(pending)))
			}
			return
		}
		if errorsIsEOF(err) {
			select {
			case <-ctx.Done():
			case <-time.After(poll):
			}
		}
	}
}

func emitCompleteLines(output chan<- rawRecord, stream Stream, pending []byte, continued bool, maxLine int, pendingStart int64) ([]byte, bool, int64) {
	for {
		newline := bytes.IndexByte(pending, '\n')
		if newline >= 0 && newline <= maxLine {
			line := pending[:newline]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			nextStart := pendingStart + int64(newline+1)
			emitLine(output, stream, line, continued, nextStart)
			pending, continued = pending[newline+1:], false
			pendingStart = nextStart
			continue
		}
		if len(pending) > maxLine {
			nextStart := pendingStart + int64(maxLine)
			emitLine(output, stream, pending[:maxLine], true, nextStart)
			pending, continued = pending[maxLine:], true
			pendingStart = nextStart
			continue
		}
		return pending, continued, pendingStart
	}
}

func emitLine(output chan<- rawRecord, stream Stream, line []byte, truncated bool, sourceEnd int64) {
	output <- rawRecord{stream: stream, message: decodeLogLine(line), truncated: truncated, sourceEnd: sourceEnd}
}

func errorsIsEOF(err error) bool { return err == io.EOF }
