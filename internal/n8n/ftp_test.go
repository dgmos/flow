package n8n

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/dgmos/flow/internal/node"
)

// sftpServer serves an in-memory SFTP file system over SSH.
func sftpServer(t *testing.T) (string, int) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) == "pw" {
			return nil, nil
		}
		return nil, ssh.ErrNoAuth
	}}
	cfg.AddHostKey(signer)
	handlers := sftp.InMemHandler()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
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
							_ = r.Reply(r.Type == "subsystem", nil)
							if r.Type == "subsystem" {
								go func() {
									_ = sftp.NewRequestServer(ch, handlers).Serve()
									_ = ch.Close()
								}()
							}
						}
					}()
				}
			}()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func TestFTPNodeSFTP(t *testing.T) {
	host, port := sftpServer(t)
	creds := NewStaticCredentials()
	creds.Set("sftp", "*", map[string]interface{}{"host": host, "port": float64(port), "username": "u", "password": "pw"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	sftpNode := func(name string, p map[string]interface{}) map[string]interface{} {
		p["protocol"] = "sftp"
		return credNode(nd(name, "ftp", 1, p), "sftp")
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		sftpNode("Up", map[string]interface{}{"operation": "upload", "path": "/out/reports/a.txt", "binaryData": false, "fileContent": "hello"}),
		sftpNode("Rename", map[string]interface{}{"operation": "rename", "oldPath": "/out/reports/a.txt", "newPath": "/archive/b.txt", "options": map[string]interface{}{"createDirectories": true}}),
		sftpNode("List", map[string]interface{}{"operation": "list", "path": "/", "recursive": true}),
		sftpNode("Get", map[string]interface{}{"path": "/archive/b.txt"}),
	}, "Start->Up", "Up->Rename", "Rename->List", "List->Get")
	if _, rep, _ := Import(data, ImportOptions{}); !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	var paths []string
	for _, it := range itemsOf(t, steps["List"], 0) {
		paths = append(paths, fmt.Sprint(it["type"], it["path"]))
	}
	if got := strings.Join(paths, " "); !strings.Contains(got, "-/archive/b.txt") || !strings.Contains(got, "d/out/reports") || strings.Contains(got, "a.txt") {
		t.Fatalf("list: %s", got)
	}
	got := OutputItems(steps["Get"].Output)
	if len(got) == 0 || got[0].Binary["data"] == nil || toString(asMap(got[0].Binary["data"])["data"]) != "aGVsbG8=" || asMap(got[0].Binary["data"])["fileName"] != "b.txt" {
		t.Fatalf("download: %+v", got)
	}
}

// ftpServer is a minimal in-memory FTP server (passive mode, LIST, RETR,
// STOR, DELE, RNFR/RNTO, MKD).
func ftpServer(t *testing.T) (string, int, map[string]string) {
	t.Helper()
	files := map[string]string{"/in/x.csv": "a,b"}
	var mu sync.Mutex
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
				say("220 hi")
				var data net.Listener
				var from string
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
					withData := func(fn func(net.Conn)) {
						say("150 ok")
						dc, err := data.Accept()
						if err == nil {
							fn(dc)
							dc.Close()
						}
						data.Close()
						say("226 done")
					}
					mu.Lock()
					switch strings.ToUpper(cmd) {
					case "USER":
						say("331 pw")
					case "PASS":
						if arg == "pw" {
							say("230 in")
						} else {
							say("530 no")
						}
					case "EPSV":
						data, _ = net.Listen("tcp", "127.0.0.1:0")
						say(fmt.Sprintf("229 ok (|||%d|)", data.Addr().(*net.TCPAddr).Port))
					case "LIST":
						dir := strings.TrimSuffix(arg, "/")
						var lines []string
						for p, v := range files {
							if gotDir, name, _ := strings.Cut(strings.TrimPrefix(p, dir+"/"), "/"); strings.HasPrefix(p, dir+"/") && name == "" {
								lines = append(lines, fmt.Sprintf("-rw-r--r-- 1 u g %d Jan 02 2024 %s", len(v), gotDir))
							}
						}
						mu.Unlock()
						withData(func(dc net.Conn) { _, _ = io.WriteString(dc, strings.Join(lines, "\r\n")+"\r\n") })
						continue
					case "RETR":
						v := files[arg]
						mu.Unlock()
						withData(func(dc net.Conn) { _, _ = io.WriteString(dc, v) })
						continue
					case "STOR":
						mu.Unlock()
						withData(func(dc net.Conn) {
							b, _ := io.ReadAll(dc)
							mu.Lock()
							files[arg] = string(b)
							mu.Unlock()
						})
						continue
					case "DELE":
						delete(files, arg)
						say("250 ok")
					case "RNFR":
						from = arg
						say("350 ok")
					case "RNTO":
						files[arg] = files[from]
						delete(files, from)
						say("250 ok")
					case "MKD":
						say("257 ok")
					case "QUIT":
						say("221 bye")
						mu.Unlock()
						return
					case "TYPE", "OPTS":
						say("200 ok")
					default:
						say("502 no")
					}
					mu.Unlock()
				}
			}(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port, files
}

func TestFTPNodeFTP(t *testing.T) {
	host, port, files := ftpServer(t)
	creds := NewStaticCredentials()
	creds.Set("ftp", "*", map[string]interface{}{"host": host, "port": float64(port), "username": "u", "password": "pw"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(nd("List", "ftp", 1, map[string]interface{}{"operation": "list", "path": "/in"}), "ftp"),
		credNode(nd("Get", "ftp", 1, map[string]interface{}{"path": "={{ $json.path }}"}), "ftp"),
		credNode(nd("Up", "ftp", 1, map[string]interface{}{"operation": "upload", "path": "/out/y.csv"}), "ftp"),
		credNode(nd("Del", "ftp", 1, map[string]interface{}{"operation": "delete", "path": "/in/x.csv"}), "ftp"),
	}, "Start->List", "List->Get", "Get->Up", "Up->Del")
	_, steps := h.run(t, data, "")
	if l := itemsOf(t, steps["List"], 0); len(l) != 1 || l[0]["path"] != "/in/x.csv" || l[0]["size"] != 3.0 {
		t.Fatalf("list: %v", l)
	}
	if files["/out/y.csv"] != "a,b" || files["/in/x.csv"] != "" {
		t.Fatalf("files: %v", files)
	}
}
