//go:build !windows

package main

import (
	"context"
	"errors"
)

func isService() bool { return false }

func runService(func(context.Context) error) error {
	return errors.New("services are only supported on Windows")
}
