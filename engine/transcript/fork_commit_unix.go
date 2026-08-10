//go:build !windows

package transcript

import "os"

func commitNewTranscriptFile(source, target string) (bool, error) {
	if err := os.Link(source, target); err != nil {
		return false, err
	}
	if err := os.Remove(source); err != nil {
		return true, err
	}
	return true, nil
}
