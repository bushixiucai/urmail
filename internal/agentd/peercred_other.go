//go:build !linux

package agentd

import "net"

// authorizePeer is a no-op on non-Linux platforms.
//
// SO_PEERCRED is a Linux socket option. The production agent only runs on
// Linux; other platforms exist so that developers can build and run the tests
// on macOS and other systems. They must not be used to serve production
// traffic.
func authorizePeer(_ *net.UnixConn, _ *peerPolicy) error {
	return nil
}
