package n8n

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-sql/sqlexp"
	mssql "github.com/microsoft/go-mssqldb"

	"github.com/dgmos/flow/internal/node"
)

// Microsoft SQL node: Execute Query (the operation templates use). $1, $2,
// … refer to the comma-separated query parameters, as in n8n.

var mssqlPools = newPoolCache(32)

type mssqlDialer struct {
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d mssqlDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func microsoftSQLNode(ec *execCtx) ([][]Item, error) {
	op, _ := ec.str(0, "operation", "insert")
	if op != "executeQuery" {
		return nil, permanentError{fmt.Sprintf("Microsoft SQL: operation %q is not supported (executeQuery is)", op)}
	}
	c, err := ec.credential("microsoftSql")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if db := credString(c, "database"); db != "" {
		q.Set("database", db)
	}
	if toBool(c["tls"]) {
		q.Set("encrypt", "true")
	} else {
		q.Set("encrypt", "disable")
	}
	if toBool(c["allowUnauthorizedCerts"]) {
		q.Set("TrustServerCertificate", "true")
	}
	if t := toFloatDefault(c["connectTimeout"], 15000); t > 0 {
		q.Set("connection timeout", strconv.Itoa(int(t/1000)))
	}
	user := credString(c, "user")
	if d := credString(c, "domain"); d != "" {
		user = d + `\` + user
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword(user, credString(c, "password")),
		Host: net.JoinHostPort(credString(c, "server"), strconv.Itoa(int(toFloatDefault(c["port"], 1433)))), RawQuery: q.Encode()}
	dsn := u.String()
	pool, err := mssqlPools.get(secretKey(dsn+mustJSON(ec.opts.Outbound)), func() (io.Closer, error) {
		conn, err := mssql.NewConnector(dsn)
		if err != nil {
			return nil, permanentError{"Microsoft SQL: " + redactSecrets(err.Error())}
		}
		conn.Dialer = mssqlDialer{dial: node.NewOutboundDialer(ec.opts.Outbound)}
		db := sql.OpenDB(conn)
		db.SetMaxOpenConns(5)
		return db, nil
	})
	if err != nil {
		return nil, err
	}
	db := pool.(*sql.DB)
	timeout := time.Duration(toFloatDefault(c["requestTimeout"], 15000)) * time.Millisecond
	run := func(i int) ([]Item, error) {
		query, _ := ec.str(i, "query", "")
		var vals []interface{}
		if rep, _ := ec.str(i, "options.queryReplacement", ""); rep != "" {
			for _, a := range splitList(rep) {
				vals = append(vals, a)
			}
		}
		query, args, err := mssqlBind(query, vals)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ec.ctx, timeout)
		defer cancel()
		return mssqlQuery(ctx, db, query, args)
	}
	if ec.version < 1.1 {
		// Version 1 runs the first item's query once.
		out, err := run(0)
		return [][]Item{out}, err
	}
	return eachItem(ec, func(i int, _ Item) ([]Item, error) { return run(i) })
}

// mssqlBind turns $1…$N into @p1…@pN parameters; a $N past the last value
// (outside quotes) is an error, as SQL Server would read it as money.
func mssqlBind(q string, vals []interface{}) (string, []interface{}, error) {
	re := regexp.MustCompile(`\$(\d+)`)
	var args []interface{}
	for k, v := range vals {
		args = append(args, sql.Named("p"+strconv.Itoa(k+1), v))
	}
	var bad int
	out := re.ReplaceAllStringFunc(q, func(m string) string {
		n, _ := strconv.Atoi(m[1:])
		if n >= 1 && n <= len(vals) {
			return "@p" + m[1:]
		}
		return m
	})
	for _, loc := range re.FindAllStringSubmatchIndex(out, -1) {
		if strings.Count(out[:loc[0]], "'")%2 == 0 {
			n, _ := strconv.Atoi(out[loc[2]:loc[3]])
			bad = max(bad, n)
		}
	}
	if bad > 0 {
		return "", nil, permanentError{fmt.Sprintf("The query references $%d, but only %d query parameter(s) are set", bad, len(vals))}
	}
	return out, args, nil
}

// mssqlQuery returns the rows of every result set, or one item per
// statement's affected-row count when nothing returns rows.
func mssqlQuery(ctx context.Context, db *sql.DB, q string, args []interface{}) ([]Item, error) {
	ret := &sqlexp.ReturnMessage{}
	rows, err := db.QueryContext(ctx, q, append(args, ret)...)
	if err != nil {
		return nil, permanentError{"Microsoft SQL: " + err.Error()}
	}
	defer func() { _ = rows.Close() }()
	var out []Item
	var counts []int64
	sets := 0
	for active := true; active; {
		switch m := ret.Message(ctx).(type) {
		case sqlexp.MsgNext:
			cols, _ := rows.Columns()
			if len(cols) > 0 {
				sets++
			}
			for rows.Next() {
				vals := make([]interface{}, len(cols))
				ptrs := make([]interface{}, len(cols))
				for k := range vals {
					ptrs[k] = &vals[k]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return nil, err
				}
				j := map[string]interface{}{}
				for k, c := range cols {
					switch v := vals[k].(type) {
					case []byte:
						j[c] = string(v)
					case time.Time:
						j[c] = v.Format(time.RFC3339Nano)
					case int64:
						j[c] = float64(v)
					default:
						j[c] = v
					}
				}
				out = append(out, Item{JSON: j})
			}
		case sqlexp.MsgRowsAffected:
			counts = append(counts, m.Count)
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case sqlexp.MsgError:
			return nil, permanentError{"Microsoft SQL: " + m.Error.Error()}
		case nil:
			active = false
		}
	}
	if sets > 0 {
		return out, rows.Err()
	}
	if len(counts) > 0 {
		for k, n := range counts {
			out = append(out, Item{JSON: map[string]interface{}{"message": fmt.Sprintf("Query %d executed successfully", k+1), "rowsAffected": float64(n)}})
		}
		return out, nil
	}
	return []Item{{JSON: map[string]interface{}{"message": "Query executed successfully, but no rows were affected"}}}, nil
}
