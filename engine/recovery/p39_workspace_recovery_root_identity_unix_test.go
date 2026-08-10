//go:build darwin || linux

package recovery

import (
	"fmt"
	"os"
	"syscall"
)

func p390DurableRootIdentity(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported root identity type %T", info.Sys())
	}
	return fmt.Sprintf("%v:%v", stat.Dev, stat.Ino), nil
}
