package n8n

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dgmos/flow/internal/node"
)

// sshNode is n8n's SSH node: it runs a command on a remote host. The
// connection goes through the outbound policy. Like n8n, the host key is
// not verified.
func sshNode(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "command", map[string]string{"command": "execute", "file": "upload"})
		if r != "command" || o != "execute" {
			return nil, unsupportedOp(ec.node.Name, r, o)
		}
		client, err := ec.sshClient(i)
		if err != nil {
			return nil, err
		}
		defer func() { _ = client.Close() }()
		cmd, _ := ec.str(i, "command", "")
		if cwd, _ := ec.str(i, "cwd", "/"); cwd != "" {
			cmd = "cd " + shellQuote(cwd) + " && " + cmd
		}
		sess, err := client.NewSession()
		if err != nil {
			return nil, err
		}
		defer func() { _ = sess.Close() }()
		max := int(ec.opts.Outbound.MaxResponseBytesOrDefault())
		stdout, stderr := &limitedBuffer{max: max}, &limitedBuffer{max: max}
		sess.Stdout, sess.Stderr = stdout, stderr
		done := make(chan error, 1)
		go func() { done <- sess.Run(cmd) }()
		var runErr error
		select {
		case runErr = <-done:
		case <-ec.ctx.Done():
			_ = sess.Signal(ssh.SIGKILL)
			return nil, ec.ctx.Err()
		}
		out := map[string]interface{}{"code": 0.0, "signal": nil,
			"stdout": strings.TrimRight(stdout.buf.String(), "\n"), "stderr": strings.TrimRight(stderr.buf.String(), "\n")}
		var exit *ssh.ExitError
		switch {
		case errors.As(runErr, &exit):
			out["code"] = float64(exit.ExitStatus())
			if s := exit.Signal(); s != "" {
				out["signal"] = s
			}
		case runErr != nil:
			return nil, fmt.Errorf("SSH: %w", runErr)
		}
		return []Item{{JSON: out}}, nil
	})
}

func (ec *execCtx) sshClient(i int) (*ssh.Client, error) {
	auth, _ := ec.str(i, "authentication", "password")
	credType := "sshPassword"
	if auth == "privateKey" {
		credType = "sshPrivateKey"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	return ec.sshDial(c, credType == "sshPrivateKey")
}

// sshDial connects with an SSH credential: a private key (with an optional
// passphrase) when withKey, otherwise a password.
func (ec *execCtx) sshDial(c map[string]interface{}, withKey bool) (*ssh.Client, error) {
	var err error
	cfg := &ssh.ClientConfig{
		User:            credString(c, "username"),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // n8n does not verify SSH host keys either
		Timeout:         15 * time.Second,
	}
	if withKey {
		key := []byte(strings.ReplaceAll(credString(c, "privateKey"), `\n`, "\n"))
		var signer ssh.Signer
		if p := credString(c, "passphrase"); p != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(p))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, permanentError{"SSH: invalid private key: " + err.Error()}
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		pw := credString(c, "password")
		cfg.Auth = []ssh.AuthMethod{ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for k := range ans {
				ans[k] = pw
			}
			return ans, nil
		})}
	}
	port := int(toFloatDefault(c["port"], 22))
	addr := net.JoinHostPort(credString(c, "host"), strconv.Itoa(port))
	conn, err := node.NewOutboundDialer(ec.opts.Outbound)(ec.ctx, "tcp", addr)
	if err != nil {
		return nil, sanitizeErr(err)
	}
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
