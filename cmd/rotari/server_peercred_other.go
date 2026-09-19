//go:build !linux

package main

import "net"

// verifyPeerCredential is a no-op outside Linux: SO_PEERCRED is Linux-specific.
// The socket's 0600 mode remains the primary access control on other platforms.
func verifyPeerCredential(conn net.Conn) error {
	return nil
}
