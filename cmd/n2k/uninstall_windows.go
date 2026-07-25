//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

const windowsUninstallScript = `@echo off
for /L %%i in (1,1,120) do (
  del /F /Q "%~1" >NUL 2>&1
  if not exist "%~1" goto done
  ping -n 2 127.0.0.1 >NUL
)
exit /B 1
:done
del /F /Q "%~f0" >NUL 2>&1
`

func removeRunningExecutable(path string) (bool, error) {
	if err := os.Remove(path); err == nil {
		return false, nil
	}

	pendingPath := fmt.Sprintf("%s.uninstall-%d-%d", path, os.Getpid(), time.Now().UnixNano())
	renamed := os.Rename(path, pendingPath) == nil
	if !renamed {
		pendingPath = path
	}

	script, err := os.CreateTemp("", "n2k-uninstall-*.cmd")
	if err != nil {
		if renamed {
			_ = os.Rename(pendingPath, path)
		}
		return false, fmt.Errorf("creating cleanup helper: %w", err)
	}
	scriptPath := script.Name()
	if _, err := script.WriteString(windowsUninstallScript); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		if renamed {
			_ = os.Rename(pendingPath, path)
		}
		return false, fmt.Errorf("writing cleanup helper: %w", err)
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		if renamed {
			_ = os.Rename(pendingPath, path)
		}
		return false, fmt.Errorf("closing cleanup helper: %w", err)
	}

	command := exec.Command("cmd.exe", "/D", "/Q", "/C", scriptPath, pendingPath) // #nosec G204 -- cmd.exe and the generated helper are fixed; only the current executable path is passed as data.
	if err := command.Start(); err != nil {
		_ = os.Remove(scriptPath)
		if renamed {
			if rollbackErr := os.Rename(pendingPath, path); rollbackErr != nil {
				return false, fmt.Errorf("starting cleanup helper: %w (rollback also failed: %v)", err, rollbackErr)
			}
		}
		return false, fmt.Errorf("starting cleanup helper: %w", err)
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	return true, nil
}
