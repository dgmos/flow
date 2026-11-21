package n8n

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/dgmos/flow/internal/node"
)

// fakeSSHServer answers exec requests with "ran: <command>" and exit code 3
// without running anything.
func fakeSSHServer(t *testing.T) (host string, port int) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "deploy" && string(pw) == "pw" {
			return nil, nil
		}
		return nil, ssh.ErrNoAuth
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						for r := range creqs {
							if r.Type != "exec" {
								_ = r.Reply(false, nil)
								continue
							}
							n := binary.BigEndian.Uint32(r.Payload[:4])
							_ = r.Reply(true, nil)
							_, _ = ch.Write([]byte("ran: " + string(r.Payload[4:4+n]) + "\n"))
							_, _ = ch.Stderr().Write([]byte("warn\n"))
							_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 3})
							_ = ch.Close()
						}
					}()
				}
			}()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func TestSSHNode(t *testing.T) {
	host, port := fakeSSHServer(t)
	creds := NewStaticCredentials()
	creds.Set("sshPassword", "*", map[string]interface{}{"host": host, "port": float64(port), "username": "deploy", "password": "pw"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(nd("SSH", "ssh", 1, map[string]interface{}{"command": "ls -la", "cwd": "/srv/app's"}), "sshPassword"),
	}, "Start->SSH")
	_, steps := h.run(t, data, "")
	got := itemsOf(t, steps["SSH"], 0)
	if len(got) != 1 || got[0]["code"] != 3.0 || got[0]["stderr"] != "warn" || !strings.HasSuffix(toString(got[0]["stdout"]), `&& ls -la`) ||
		!strings.Contains(toString(got[0]["stdout"]), `cd '/srv/app'\''s'`) {
		t.Fatalf("ssh: %v", got)
	}
}
