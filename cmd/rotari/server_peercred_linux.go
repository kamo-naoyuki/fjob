package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// verifyPeerCredential rejects unix-socket connections from a UID other than
// the server's own, defending in depth alongside the socket's 0600 mode.
func verifyPeerCredential(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if uid := uint32(os.Getuid()); cred.Uid != uid {
		return fmt.Errorf("rejected connection from uid %d (server runs as uid %d)", cred.Uid, uid)
	}
	return nil
}
