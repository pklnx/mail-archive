//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// runAs makes cmd run as uid and gid, without supplementary groups.
func runAs(cmd *exec.Cmd, uid, gid int) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: true},
	}
	return nil
}

func fileOwner(path string) (uint32, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Sys().(*syscall.Stat_t).Uid, nil
}
