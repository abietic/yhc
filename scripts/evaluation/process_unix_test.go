//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestOwnedProcessTimeoutTerminatesDescendants(t *testing.T) {
	pidPath := privateTempDir(t) + "/child.pid"
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	command := exec.Command(os.Args[0], "-test.run=TestEvaluationProcessHelper")
	command.Env = append(os.Environ(), "EINO_EVAL_HELPER=parent", "EINO_EVAL_PID_PATH="+pidPath)
	err := runOwnedCommand(ctx, command)
	if errorCode(err) != "process_timeout" {
		t.Fatalf("timeout error=%v", err)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived owned process timeout", pid)
}

func TestOwnedProcessCancellationIsDistinct(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.Command(os.Args[0], "-test.run=TestEvaluationProcessHelper")
	command.Env = append(os.Environ(), "EINO_EVAL_HELPER=child")
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := runOwnedCommand(ctx, command); errorCode(err) != "process_canceled" {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestEvaluationProcessHelper(t *testing.T) {
	switch os.Getenv("EINO_EVAL_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestEvaluationProcessHelper")
		child.Env = append(os.Environ(), "EINO_EVAL_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(91)
		}
		if err := os.WriteFile(os.Getenv("EINO_EVAL_PID_PATH"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(92)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	}
}
