//go:build darwin

package containment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// CaptureRootIdentity resolves symlinks before pinning the Darwin filesystem object.
func CaptureRootIdentity(path string) (RootIdentity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return RootIdentity{}, errSeatbeltRootChanged
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return RootIdentity{}, errSeatbeltRootChanged
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return RootIdentity{}, errSeatbeltRootChanged
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return RootIdentity{}, errSeatbeltRootChanged
	}
	return RootIdentity{Path: resolved, Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func verifySeatbeltExecutable() error {
	info, err := os.Lstat(darwinSeatbeltExecutable)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errSeatbeltExecutable
	}
	return nil
}

func runSeatbeltSpawn(ctx context.Context, spawn SpawnSpec) error {
	bounded, cancel := boundedSeatbeltContext(ctx)
	defer cancel()
	command := exec.CommandContext(bounded, spawn.Path, spawn.Args...)
	command.Dir, command.Env = spawn.Dir, spawn.Env
	if err := command.Run(); err != nil {
		return errSeatbeltProbe
	}
	return nil
}
