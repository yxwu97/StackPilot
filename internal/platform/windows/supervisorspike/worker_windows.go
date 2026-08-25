//go:build windows

package supervisorspike

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func runWorker(args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	depth := flags.Int("depth", 0, "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *depth > 0 {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve worker executable: %w", err)
		}
		child := exec.Command(executable, "worker", "--depth", strconv.Itoa(*depth-1))
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
		if err := child.Start(); err != nil {
			return fmt.Errorf("start child worker: %w", err)
		}
		if err := child.Process.Release(); err != nil {
			return fmt.Errorf("release child worker: %w", err)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
