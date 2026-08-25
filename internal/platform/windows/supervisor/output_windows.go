//go:build windows

package supervisor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

const outputReadBufferSize = 32 * 1024

type managedOutput struct {
	reader *os.File
	writer *os.File
	spool  *os.File
	done   chan error
}

func openManagedOutput(instanceDir, spoolPath string) (*managedOutput, error) {
	spool, err := openSpoolFile(instanceDir, spoolPath)
	if err != nil {
		return nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(spool.Fd()), windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = spool.Close()
		return nil, fmt.Errorf("make managed spool private: %w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create managed output pipe: %w", err), spool.Close())
	}
	if err := windows.SetHandleInformation(windows.Handle(writer.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return nil, errors.Join(fmt.Errorf("make managed output pipe inheritable: %w", err), reader.Close(), writer.Close(), spool.Close())
	}
	return &managedOutput{reader: reader, writer: writer, spool: spool, done: make(chan error, 1)}, nil
}

func (output *managedOutput) start(secretValues [][]byte) {
	_ = output.writer.Close()
	output.writer = nil
	go func() {
		err := redactOutputStream(output.reader, output.spool, secretValues)
		output.done <- errors.Join(err, output.reader.Close())
		clearOutputSecrets(secretValues)
	}()
}

func (output *managedOutput) abort() error {
	return errors.Join(closeOutputFile(output.reader), closeOutputFile(output.writer), closeOutputFile(output.spool))
}

func (output *managedOutput) close() error {
	var err error
	if output.done != nil {
		err = <-output.done
		output.done = nil
	}
	return errors.Join(err, closeOutputFile(output.spool))
}

func closeOutputFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func redactOutputStream(reader io.Reader, writer io.Writer, secretValues [][]byte) error {
	pending := make([]byte, 0, outputReadBufferSize)
	buffer := make([]byte, outputReadBufferSize)
	for {
		count, readErr := reader.Read(buffer)
		pending = append(pending, buffer[:count]...)
		var err error
		pending, err = writeRedactedOutput(writer, pending, secretValues, readErr == io.EOF)
		if err != nil {
			return err
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("read managed process output: %w", readErr)
		}
	}
}

func writeRedactedOutput(writer io.Writer, pending []byte, secretValues [][]byte, final bool) ([]byte, error) {
	for {
		index, value := firstOutputSecret(pending, secretValues)
		if index < 0 {
			keep := outputSecretPrefixLength(pending, secretValues)
			if final {
				keep = 0
			}
			if err := writeOutputBytes(writer, pending[:len(pending)-keep]); err != nil {
				return nil, err
			}
			return append([]byte(nil), pending[len(pending)-keep:]...), nil
		}
		if err := writeOutputBytes(writer, pending[:index]); err != nil {
			return nil, err
		}
		if err := writeOutputBytes(writer, []byte("[REDACTED:secret]")); err != nil {
			return nil, err
		}
		pending = pending[index+len(value):]
	}
}

func firstOutputSecret(pending []byte, values [][]byte) (int, []byte) {
	index, selected := -1, []byte(nil)
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		candidate := bytes.Index(pending, value)
		if candidate >= 0 && (index < 0 || candidate < index || (candidate == index && len(value) > len(selected))) {
			index, selected = candidate, value
		}
	}
	return index, selected
}

func outputSecretPrefixLength(pending []byte, values [][]byte) int {
	maximum := 0
	for _, value := range values {
		if matched := trailingSecretPrefixLength(pending, value); matched > maximum {
			maximum = matched
		}
	}
	return maximum
}

func trailingSecretPrefixLength(value, pattern []byte) int {
	if len(pattern) < 2 {
		return 0
	}
	prefixes := secretPrefixTable(pattern)
	matched := 0
	for _, current := range value {
		for matched > 0 && pattern[matched] != current {
			matched = prefixes[matched-1]
		}
		if pattern[matched] == current {
			matched++
		}
		if matched == len(pattern) {
			matched = prefixes[matched-1]
		}
	}
	return matched
}

func secretPrefixTable(pattern []byte) []int {
	result := make([]int, len(pattern))
	matched := 0
	for index := 1; index < len(pattern); index++ {
		for matched > 0 && pattern[index] != pattern[matched] {
			matched = result[matched-1]
		}
		if pattern[index] == pattern[matched] {
			matched++
		}
		result[index] = matched
	}
	return result
}

func writeOutputBytes(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return fmt.Errorf("write redacted process spool: %w", err)
		}
		if written == 0 {
			return fmt.Errorf("write redacted process spool: %w", io.ErrShortWrite)
		}
		value = value[written:]
	}
	return nil
}

func clearOutputSecrets(values [][]byte) {
	for _, value := range values {
		for index := range value {
			value[index] = 0
		}
	}
}
