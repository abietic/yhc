//go:build !darwin && !linux

package filehistory

import "fmt"

func coldExchange(int, string, string) error { return fmt.Errorf("history cold exchange unsupported") }
