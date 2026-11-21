package n8n

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	gopath "path"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/pkg/sftp"

	"github.com/dgmos/flow/internal/node"
)

// ftpNode is n8n's FTP node over FTP or SFTP: list, download, upload,
// rename and delete. Connections go through the outbound policy.

// remoteFS is what the node needs from an FTP or SFTP connection.
type remoteFS interface {
	list(p string) ([]map[string]interface{}, error)
	get(p string, max int64) ([]byte, error)
	put(p string, data []byte) error
	remove(p string) error
	removeDir(p string, recursive bool) error
	rename(from, to string) error
	mkdirAll(dir string) error
	close()
}

func ftpNode(ec *execCtx) ([][]Item, error) {
	protocol, _ := ec.str(0, "protocol", "ftp")
	op, _ := ec.str(0, "operation", "download")
	timeout := time.Duration(toFloatDefault(mustParam(ec, "options.timeout", 10000.0), 10000)) * time.Millisecond
	var fs remoteFS
	var err error
	if protocol == "sftp" {
		fs, err = ec.openSFTP(timeout)
	} else {
		fs, err = ec.openFTP(timeout)
	}
	if err != nil {
		return nil, err
	}
	defer fs.close()
	max := ec.opts.Outbound.MaxResponseBytesOrDefault()
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		p, _ := ec.str(i, "path", "")
		opts := fieldsOf(ec, i, "options")
		switch op {
		case "list":
			if p == "" {
				p = "/"
			}
			recursive, _ := ec.boolean(i, "recursive", false)
			var out []Item
			dirs := []string{p}
			for k := 0; k < len(dirs) && k < 10000; k++ {
				entries, err := fs.list(dirs[k])
				if err != nil {
					return nil, err
				}
				for _, e := range entries {
					name := toString(e["name"])
					if name == "." || name == ".." {
						continue
					}
					e["path"] = strings.TrimSuffix(dirs[k], "/") + "/" + name
					if recursive && e["type"] == "d" {
						dirs = append(dirs, toString(e["path"]))
					}
					out = append(out, Item{JSON: e})
				}
				if !recursive {
					break
				}
			}
			return out, nil
		case "download":
			data, err := fs.get(p, max)
			if err != nil {
				return nil, err
			}
			prop, _ := ec.str(i, "binaryPropertyName", "data")
			name := gopath.Base(p)
			mt := mime.TypeByExtension(gopath.Ext(name))
			if mt == "" {
				mt = "application/octet-stream"
			}
			res := Item{JSON: it.JSON, Binary: map[string]interface{}{}}
			for k, v := range it.Binary {
				res.Binary[k] = v
			}
			res.Binary[prop] = binaryData(data, strings.Split(mt, ";")[0], name)
			return []Item{res}, nil
		case "upload":
			var data []byte
			if bin, _ := ec.boolean(i, "binaryData", true); bin {
				prop, _ := ec.str(i, "binaryPropertyName", "data")
				d, _, _, err := binaryOf(it, prop)
				if err != nil {
					return nil, err
				}
				data = d
			} else {
				s, _ := ec.str(i, "fileContent", "")
				data = []byte(s)
			}
			if err := fs.mkdirAll(gopath.Dir(p)); err != nil {
				return nil, err
			}
			if err := fs.put(p, data); err != nil {
				return nil, err
			}
			return []Item{{JSON: it.JSON, Binary: it.Binary}}, nil
		case "delete":
			if toBool(opts["folder"]) {
				err = fs.removeDir(p, toBool(opts["recursive"]))
			} else {
				err = fs.remove(p)
			}
			if err != nil {
				return nil, err
			}
			return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
		case "rename":
			from, _ := ec.str(i, "oldPath", "")
			to, _ := ec.str(i, "newPath", "")
			if toBool(opts["createDirectories"]) {
				if err := fs.mkdirAll(gopath.Dir(to)); err != nil {
					return nil, err
				}
			}
			if err := fs.rename(from, to); err != nil {
				return nil, err
			}
			return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
		}
		return nil, permanentError{fmt.Sprintf("FTP: operation %q is not supported", op)}
	})
}

// ---------------------------------------------------------------- FTP

type ftpFS struct{ c *ftp.ServerConn }

func (ec *execCtx) openFTP(timeout time.Duration) (remoteFS, error) {
	c, err := ec.credential("ftp")
	if err != nil {
		return nil, err
	}
	port := int(toFloatDefault(c["port"], 21))
	addr := net.JoinHostPort(credString(c, "host"), strconv.Itoa(port))
	dial := node.NewOutboundDialer(ec.opts.Outbound)
	conn, err := ftp.Dial(addr, ftp.DialWithTimeout(timeout), ftp.DialWithContext(ec.ctx),
		ftp.DialWithDialFunc(func(network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ec.ctx, timeout)
			defer cancel()
			return dial(ctx, network, address)
		}))
	if err != nil {
		return nil, fmt.Errorf("FTP: %w", sanitizeErr(err))
	}
	if err := conn.Login(firstNonEmpty(credString(c, "username"), "anonymous"), credString(c, "password")); err != nil {
		_ = conn.Quit()
		return nil, fmt.Errorf("FTP: %w", err)
	}
	return &ftpFS{c: conn}, nil
}

func (f *ftpFS) list(p string) ([]map[string]interface{}, error) {
	entries, err := f.c.List(p)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		typ := "-"
		switch e.Type {
		case ftp.EntryTypeFolder:
			typ = "d"
		case ftp.EntryTypeLink:
			typ = "l"
		}
		out = append(out, map[string]interface{}{"type": typ, "name": e.Name, "size": float64(e.Size), "target": e.Target,
			"modifyTime": e.Time.UTC().Format(time.RFC3339)})
	}
	return out, nil
}

func (f *ftpFS) get(p string, max int64) ([]byte, error) {
	r, err := f.c.Retr(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return node.ReadLimited(r, max)
}

func (f *ftpFS) put(p string, data []byte) error { return f.c.Stor(p, bytes.NewReader(data)) }
func (f *ftpFS) remove(p string) error           { return f.c.Delete(p) }
func (f *ftpFS) rename(a, b string) error        { return f.c.Rename(a, b) }

func (f *ftpFS) removeDir(p string, recursive bool) error {
	if recursive {
		return f.c.RemoveDirRecur(p)
	}
	return f.c.RemoveDir(p)
}

func (f *ftpFS) mkdirAll(dir string) error {
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	cur := ""
	if strings.HasPrefix(dir, "/") {
		cur = "/"
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		cur = gopath.Join(cur, part)
		_ = f.c.MakeDir(cur) // exists already, or fails on put below
	}
	return nil
}

func (f *ftpFS) close() { _ = f.c.Quit() }

// ---------------------------------------------------------------- SFTP

type sftpFS struct {
	c      *sftp.Client
	closer func()
}

func (ec *execCtx) openSFTP(timeout time.Duration) (remoteFS, error) {
	c, err := ec.credential("sftp")
	if err != nil {
		return nil, err
	}
	sc, err := ec.sshDial(c, credString(c, "privateKey") != "")
	if err != nil {
		return nil, err
	}
	cl, err := sftp.NewClient(sc)
	if err != nil {
		_ = sc.Close()
		return nil, fmt.Errorf("SFTP: %w", err)
	}
	_ = timeout
	return &sftpFS{c: cl, closer: func() { _ = cl.Close(); _ = sc.Close() }}, nil
}

func (s *sftpFS) list(p string) ([]map[string]interface{}, error) {
	infos, err := s.c.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(infos))
	for _, fi := range infos {
		typ := "-"
		if fi.IsDir() {
			typ = "d"
		} else if fi.Mode()&os.ModeSymlink != 0 {
			typ = "l"
		}
		m := map[string]interface{}{"type": typ, "name": fi.Name(), "size": float64(fi.Size()), "modifyTime": float64(fi.ModTime().UnixMilli()),
			"rights": sftpRights(fi.Mode())}
		if st, ok := fi.Sys().(*sftp.FileStat); ok {
			m["accessTime"] = float64(int64(st.Atime) * 1000)
			m["owner"] = float64(st.UID)
			m["group"] = float64(st.GID)
		}
		out = append(out, m)
	}
	return out, nil
}

func sftpRights(m os.FileMode) map[string]interface{} {
	part := func(shift uint) string {
		var b strings.Builder
		for k, c := range "rwx" {
			if m&(1<<(shift+2-uint(k))) != 0 {
				b.WriteRune(c)
			}
		}
		return b.String()
	}
	return map[string]interface{}{"user": part(6), "group": part(3), "other": part(0)}
}

func (s *sftpFS) get(p string, max int64) ([]byte, error) {
	f, err := s.c.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return node.ReadLimited(f, max)
}

func (s *sftpFS) put(p string, data []byte) error {
	f, err := s.c.Create(p)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *sftpFS) remove(p string) error    { return s.c.Remove(p) }
func (s *sftpFS) rename(a, b string) error { return s.c.Rename(a, b) }
func (s *sftpFS) mkdirAll(dir string) error {
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	return s.c.MkdirAll(dir)
}

func (s *sftpFS) removeDir(p string, recursive bool) error {
	if recursive {
		return s.c.RemoveAll(p)
	}
	return s.c.RemoveDirectory(p)
}

func (s *sftpFS) close() { s.closer() }
