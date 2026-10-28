//go:build !linux

package sievegate

import (
	"errors"
	"net"
)

func authorize(_ *net.UnixConn, _ uint32) error {
	return errors.New("sieve gate requires Linux peer credentials")
}
