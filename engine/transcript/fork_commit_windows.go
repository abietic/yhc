//go:build windows

package transcript

import "golang.org/x/sys/windows"

func commitNewTranscriptFile(source, target string) (bool, error) {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return false, err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false, err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return false, err
	}
	return true, nil
}
