//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "os"

// Recovery fails closed when file ownership cannot be verified.
func ownedByOperator(os.FileInfo) bool { return false }
