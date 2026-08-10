//go:build windows

package tools

import (
	"os"

	"golang.org/x/sys/windows"
)

func configFileHasSingleLink(file *os.File) bool {
	if file == nil {
		return false
	}
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
	return err == nil && info.NumberOfLinks == 1
}
