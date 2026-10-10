//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"
	"syscall"
	"testing"
)

type ownerInfo struct {
	os.FileInfo
	uid uint32
}

func (i ownerInfo) Sys() any { return &syscall.Stat_t{Uid: i.uid} }

func TestAllowlistRequiresCurrentOperatorOwnership(t *testing.T) {
	current := uint32(os.Geteuid())
	if !ownedByOperator(ownerInfo{uid: current}) {
		t.Fatal("current operator rejected")
	}
	if ownedByOperator(ownerInfo{uid: current + 1}) {
		t.Fatal("different owner accepted")
	}
}
