// Package sqlitejson runs one read-only SQLite query and returns its rows as
// JSON, in the shape of `sqlite3 -readonly -json`.
//
// Firekeeper reads provider databases through the sqlite3 command line tool
// where it exists. Windows does not ship one, and asking users to install it
// would break the "download and run" setup, so Run falls back to an
// in-process read-only connection when sqlite3 is not on PATH. Both paths
// take the same arguments and produce the same JSON, so callers do not
// care which one ran.
package sqlitejson

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// busyTimeout bounds how long a locked database is retried, as the sqlite3
// shell would without a .timeout.
const busyTimeout = 2 * time.Second

// ErrArguments means Run was not given `-readonly -json <path> <query>`.
var ErrArguments = errors.New("sqlitejson: expected -readonly -json <database> <query>")

// Run runs the sqlite3 command line tool with args when it is on PATH, and
// answers the same request in-process otherwise. args must be exactly
// "-readonly", "-json", the database path, and the query. A query that
// returns no rows produces no output, like sqlite3.
//
// With the command line tool, a failure is the *exec.ExitError sqlite3
// produced. In-process failures are sanitized: they never include the query.
func Run(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("sqlite3"); err == nil {
		return exec.CommandContext(ctx, "sqlite3", args...).Output()
	}
	return RunInProcess(ctx, args...)
}

// RunInProcess answers a Run request without the sqlite3 command line tool.
func RunInProcess(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) != 4 || args[0] != "-readonly" || args[1] != "-json" {
		return nil, ErrArguments
	}
	path, query := args[2], args[3]
	if _, err := os.Stat(path); err != nil {
		// Opening a missing file read-only would fail later with a message
		// naming the full path; say it once, without it.
		return nil, errors.New("sqlitejson: database is not readable")
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, errors.New("sqlitejson: open database")
	}
	defer db.Close()
	// One connection keeps the busy timeout and read-only mode in force.
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, queryError(ctx, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, queryError(ctx, err)
	}

	var out bytes.Buffer
	count := 0
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return nil, queryError(ctx, err)
		}
		if count == 0 {
			out.WriteString("[")
		} else {
			out.WriteString(",\n")
		}
		if err := writeRow(&out, columns, values); err != nil {
			return nil, errors.New("sqlitejson: encode row")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, queryError(ctx, err)
	}
	if count == 0 {
		return nil, nil
	}
	out.WriteString("]\n")
	return out.Bytes(), nil
}

// queryError reports a failed query without the driver's message, which can
// quote the SQL or the database path.
func queryError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("sqlitejson: query: %w", ctxErr)
	}
	return errors.New("sqlitejson: query failed")
}

// readOnlyDSN builds a file: URI that opens path read-only. SQLite reads
// "file:///C:/dir/db" as a Windows path, so drive paths gain a leading slash.
func readOnlyDSN(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	return u.String()
}

// writeRow appends one JSON object with its keys in column order, which a
// Go map would lose.
func writeRow(out *bytes.Buffer, columns []string, values []any) error {
	out.WriteString("{")
	for i, column := range columns {
		if i > 0 {
			out.WriteString(",")
		}
		key, err := json.Marshal(column)
		if err != nil {
			return err
		}
		out.Write(key)
		out.WriteString(":")
		value, err := json.Marshal(jsonValue(values[i]))
		if err != nil {
			return err
		}
		out.Write(value)
	}
	out.WriteString("}")
	return nil
}

// jsonValue maps a scanned SQLite value to what sqlite3 -json prints.
func jsonValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		// The driver parses columns declared as dates; sqlite3 would have
		// printed the stored text.
		return x.Format(time.RFC3339Nano)
	default:
		return v
	}
}
