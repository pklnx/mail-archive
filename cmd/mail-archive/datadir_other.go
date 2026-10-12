//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

var errNotSupported = errors.New("not supported on this system")

func runAs(*exec.Cmd, int, int) error { return errNotSupported }

func fileOwner(string) (uint32, error) { return 0, errNotSupported }
