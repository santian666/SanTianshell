//go:build !windows

package sshagent

import (
	"context"
	"net"
	"os"
	"strings"
)

func platformCandidates() []dialCandidate {
	address := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	if address == "" {
		return nil
	}
	return []dialCandidate{{
		source:  SourceEnvironment,
		address: address,
		dial: func(ctx context.Context) (agentConnection, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", address)
		},
	}}
}

func detectPageant() PageantStatus {
	return PageantStatus{Reason: "Pageant 仅适用于 Windows"}
}
