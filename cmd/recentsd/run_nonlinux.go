//go:build !linux

package main

import (
	"context"
	"errors"
)

func run(context.Context) error {
	return errors.New("recentsd currently supports Linux only")
}
