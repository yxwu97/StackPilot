// Process fixture provides deterministic lifecycle behaviors for Windows integration tests.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type config struct {
	mode        string
	port        int
	delay       time.Duration
	marker      string
	environment string
	bytes       int
	exitCode    int
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-version" {
		_, _ = fmt.Fprintln(os.Stderr, `openjdk version "21.0.10" 2026-01-01`)
		return
	}
	configuration := parseConfig()
	if err := run(configuration); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(70)
	}
}

func parseConfig() config {
	value := config{}
	flag.StringVar(&value.mode, "mode", "", "fixture behavior")
	flag.IntVar(&value.port, "port", 0, "loopback TCP port")
	flag.DurationVar(&value.delay, "delay", 250*time.Millisecond, "readiness delay")
	flag.StringVar(&value.marker, "marker", "", "fixture marker path")
	flag.StringVar(&value.environment, "environment", "", "environment variable to print")
	flag.IntVar(&value.bytes, "bytes", 1024*1024, "bytes per output stream")
	flag.IntVar(&value.exitCode, "exit-code", 23, "immediate exit code")
	flag.Parse()
	return value
}

func run(value config) error {
	switch value.mode {
	case "slow-ready":
		return slowReady(value)
	case "immediate-exit":
		_, _ = fmt.Fprintln(os.Stdout, "oneshot stdout before exit")
		_, _ = fmt.Fprintln(os.Stderr, "oneshot stderr before exit")
		os.Exit(value.exitCode)
		return nil
	case "child-tree":
		return childTree(value)
	case "child-worker", "ignore-terminate":
		return ignoreTermination()
	case "large-log":
		return largeLog(value.bytes)
	case "secret-log":
		return secretLog(value.environment)
	case "hold-port":
		return holdPort(value.port)
	default:
		return fmt.Errorf("unknown fixture mode %q", value.mode)
	}
}

func secretLog(environment string) error {
	if environment == "" {
		return errors.New("secret-log requires --environment")
	}
	value := os.Getenv(environment)
	if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
			return err
		}
	}
	return nil
}

func slowReady(value config) error {
	if value.port < 1 {
		return errors.New("slow-ready requires --port")
	}
	timer := time.NewTimer(value.delay)
	defer timer.Stop()
	<-timer.C
	return holdPort(value.port)
}

func holdPort(port int) error {
	if port < 1 {
		return errors.New("hold-port requires --port")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen fixture port: %w", err)
	}
	defer listener.Close()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		_ = connection.Close()
	}
}

func childTree(value config) error {
	if value.marker == "" {
		return errors.New("child-tree requires --marker")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "--mode", "child-worker")
	if err := command.Start(); err != nil {
		return fmt.Errorf("start fixture child: %w", err)
	}
	if err := os.WriteFile(value.marker, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		_ = command.Process.Kill()
		return err
	}
	return ignoreTermination()
}

func ignoreTermination() error {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	for {
		select {
		case <-signals:
		case <-time.After(time.Hour):
		}
	}
}

func largeLog(bytesPerStream int) error {
	if bytesPerStream < 1 || bytesPerStream > 64*1024*1024 {
		return errors.New("large-log byte count is outside fixture bounds")
	}
	if err := writeBytes(os.Stdout, "stdout-line\n", bytesPerStream); err != nil {
		return err
	}
	if err := writeBytes(os.Stderr, "stderr-line\n", bytesPerStream); err != nil {
		return err
	}
	return ignoreTermination()
}

func writeBytes(file *os.File, line string, total int) error {
	writer := bufio.NewWriterSize(file, 64*1024)
	written := 0
	for written < total {
		count, err := writer.WriteString(line)
		if err != nil {
			return err
		}
		written += count
	}
	return writer.Flush()
}
