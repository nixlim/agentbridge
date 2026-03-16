package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const processGroupKillGrace = 2 * time.Second

type CommandExecutionError struct {
	Cause    error
	Stdout   string
	Stderr   string
	ExitCode int
}

func (e *CommandExecutionError) Error() string {
	if e == nil {
		return ""
	}
	base := ""
	if e.Cause != nil {
		base = e.Cause.Error()
	}
	detail := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(e.Stdout), strings.TrimSpace(e.Stderr)}, "\n"))
	switch {
	case base != "" && detail != "":
		return fmt.Sprintf("%s: %s", base, detail)
	case base != "":
		return base
	default:
		return detail
	}
}

func (e *CommandExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func executeAdapterCommand(ctx context.Context, config AgentConfig, workspacePath, workDir string, args []string, parse func([]byte) (*AgentResult, error)) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.Command(config.Command, args...)
	cmd.Dir = chooseWorkDir(workDir, config.WorkingDir, workspacePath)
	cmd.Env = append(os.Environ(), flattenEnv(config.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	observer := commandTelemetryObserverFromContext(ctx)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = telemetryMultiWriter(&stdout, "stdout", observer)
	cmd.Stderr = telemetryMultiWriter(&stderr, "stderr", observer)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if observer != nil {
			observer.Finish("start_failed", -1, err, time.Since(start))
		}
		return nil, fmt.Errorf("start command: %w", err)
	}
	if observer != nil && cmd.Process != nil {
		observer.ProcessStarted(cmd.Process.Pid)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	if observer != nil {
		observer.WaitStarted()
	}

	var err error
	exitCode := 0
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		err = stopProcessGroup(cmd, waitCh)
		if ctx.Err() == context.DeadlineExceeded {
			timeoutErr := &CommandExecutionError{
				Cause:    fmt.Errorf("timeout after %ds", config.TimeoutSeconds),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: exitCodeFromError(err),
			}
			if observer != nil {
				observer.Finish("timed_out", timeoutErr.ExitCode, timeoutErr, time.Since(start))
			}
			return nil, timeoutErr
		}
		if observer != nil {
			observer.Finish("cancelled", exitCodeFromError(err), ctx.Err(), time.Since(start))
		}
		return nil, ctx.Err()
	}

	duration := time.Since(start)
	exitCode = exitCodeFromError(err)
	if ctx.Err() == context.DeadlineExceeded {
		if observer != nil {
			observer.Finish("timed_out", exitCode, fmt.Errorf("timeout after %ds", config.TimeoutSeconds), duration)
		}
		return nil, fmt.Errorf("timeout after %ds", config.TimeoutSeconds)
	}
	if err != nil {
		commandErr := &CommandExecutionError{
			Cause:    err,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		}
		if observer != nil {
			observer.Finish("failed", exitCode, commandErr, duration)
		}
		return nil, commandErr
	}
	if observer != nil {
		observer.Finish("succeeded", exitCode, nil, duration)
	}

	result, parseErr := parse(stdout.Bytes())
	if parseErr != nil {
		return &AgentResult{
			RawOutput:  stdout.String(),
			Summary:    strings.TrimSpace(stdout.String()),
			DurationMs: duration.Milliseconds(),
		}, nil
	}
	result.DurationMs = duration.Milliseconds()
	return result, nil
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		return status.ExitStatus()
	}
	return -1
}

func stopProcessGroup(cmd *exec.Cmd, waitCh <-chan error) error {
	if cmd == nil || cmd.Process == nil {
		select {
		case err := <-waitCh:
			return err
		default:
			return nil
		}
	}

	// Kill the whole process group so child CLIs do not outlive the parent process.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

	select {
	case err := <-waitCh:
		return err
	case <-time.After(processGroupKillGrace):
	}

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return <-waitCh
}
