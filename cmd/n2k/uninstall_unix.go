//go:build !windows

package main

import "os"

func removeRunningExecutable(path string) (bool, error) {
	return false, os.Remove(path)
}
