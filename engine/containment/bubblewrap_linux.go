//go:build linux

package containment

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// CaptureRootIdentity resolves symlinks before pinning the Linux filesystem object.
func CaptureRootIdentity(path string) (RootIdentity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return RootIdentity{}, errBubblewrapRootChanged
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return RootIdentity{}, errBubblewrapRootChanged
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return RootIdentity{}, errBubblewrapRootChanged
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return RootIdentity{}, errBubblewrapRootChanged
	}
	return RootIdentity{Path: resolved, Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func verifyBubblewrapExecutable() error {
	info, err := os.Lstat(linuxBubblewrapExecutable)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return errBubblewrapExecutable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errBubblewrapExecutable
	}
	return nil
}

func newSocketDenySeccompFile() (*os.File, error) {
	arch, ok := seccompAuditArchitecture(runtime.GOARCH)
	if !ok {
		return nil, errBubblewrapUnsupported
	}
	filters := []unix.SockFilter{
		{Code: 0x20, K: 4},           // BPF_LD | BPF_W | BPF_ABS: seccomp_data.arch
		{Code: 0x15, Jt: 1, K: arch}, // BPF_JMP | BPF_JEQ | BPF_K
		{Code: 0x06, K: 0x80000000},  // BPF_RET | BPF_K: SECCOMP_RET_KILL_PROCESS
		{Code: 0x20, K: 0},           // seccomp_data.nr
	}
	for _, number := range []uint32{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND,
		unix.SYS_LISTEN, unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO,
		unix.SYS_RECVFROM, unix.SYS_SENDMSG, unix.SYS_RECVMSG, unix.SYS_SENDMMSG,
		unix.SYS_RECVMMSG, unix.SYS_SHUTDOWN, unix.SYS_GETSOCKNAME,
		unix.SYS_GETPEERNAME, unix.SYS_SETSOCKOPT, unix.SYS_GETSOCKOPT,
		unix.SYS_IO_URING_SETUP,
	} {
		filters = append(filters,
			unix.SockFilter{Code: 0x15, Jf: 1, K: number},
			unix.SockFilter{Code: 0x06, K: 0x00050000 | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, unix.SockFilter{Code: 0x06, K: 0x7fff0000}) // SECCOMP_RET_ALLOW

	fd, err := unix.MemfdCreate("yhc-socket-deny", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "yhc-socket-deny")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errBubblewrapProbe
	}
	if err := binary.Write(file, binary.LittleEndian, filters); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func seccompAuditArchitecture(arch string) (uint32, bool) {
	switch arch {
	case "amd64":
		return 0xc000003e, true // AUDIT_ARCH_X86_64
	case "arm64":
		return 0xc00000b7, true // AUDIT_ARCH_AARCH64
	default:
		return 0, false
	}
}

func runBubblewrapSpawn(ctx context.Context, spawn SpawnSpec) error {
	bounded, cancel := boundedBubblewrapContext(ctx)
	defer cancel()
	defer closeSpawnExtraFiles(spawn.ExtraFiles)
	command := exec.CommandContext(bounded, spawn.Path, spawn.Args...)
	command.Dir, command.Env = spawn.Dir, spawn.Env
	command.ExtraFiles = spawn.ExtraFiles
	if err := command.Run(); err != nil {
		return errBubblewrapProbe
	}
	return nil
}

func verifySeatbeltExecutable() error { return errSeatbeltUnsupported }

func runSeatbeltSpawn(context.Context, SpawnSpec) error { return errSeatbeltUnsupported }
