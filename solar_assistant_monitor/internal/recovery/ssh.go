package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHRebooter struct {
	host        string
	user        string
	password    string
	fingerprint string
	command     string
}

func NewSSHRebooter(host, user, password, fingerprint string) *SSHRebooter {
	return &SSHRebooter{
		host:        host,
		user:        user,
		password:    password,
		fingerprint: fingerprint,
		command:     "sudo -S -p '' reboot",
	}
}

func (r *SSHRebooter) Reboot(ctx context.Context) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", r.host)
	if err != nil {
		return fmt.Errorf("SSH dial: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: r.user,
		Auth: []ssh.AuthMethod{ssh.Password(r.password)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if actual != r.fingerprint {
				return fmt.Errorf("SSH host key mismatch: expected %s, got %s", r.fingerprint, actual)
			}
			return nil
		},
		Timeout: 10 * time.Second,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, r.host, sshConfig)
	if err != nil {
		connection.Close()
		return fmt.Errorf("SSH handshake: %w", err)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("SSH session: %w", err)
	}
	defer session.Close()
	if err := session.RequestPty("xterm", 80, 40, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		return fmt.Errorf("request SSH pty: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("SSH stdin: %w", err)
	}
	if err := session.Start(r.command); err != nil {
		return fmt.Errorf("start reboot command: %w", err)
	}
	if _, err := io.WriteString(stdin, r.password+"\n"); err != nil {
		return fmt.Errorf("supply sudo password: %w", err)
	}
	_ = stdin.Close()

	result := make(chan error, 1)
	go func() { result <- session.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if err == nil || expectedRebootDisconnect(err) {
			return nil
		}
		return fmt.Errorf("reboot command: %w", err)
	case <-time.After(15 * time.Second):
		return nil
	}
}

func expectedRebootDisconnect(err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "exit missing") || strings.Contains(message, "connection reset") || strings.Contains(message, "unexpected packet")
}
