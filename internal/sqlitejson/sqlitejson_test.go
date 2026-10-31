package sqlitejson

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fixture creates a database with one table of mixed column types.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "with space.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE threads (id TEXT, n INTEGER, ratio REAL, note TEXT, blobby BLOB)`,
		`INSERT INTO threads VALUES ('a', 1, 0.5, NULL, x'6869')`,
		`INSERT INTO threads VALUES ('b', 2, 1.5, 'two', NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRunInProcessMatchesSQLite3JSON(t *testing.T) {
	path := fixture(t)
	out, err := RunInProcess(context.Background(), "-readonly", "-json", path, "SELECT id, n, ratio, note, blobby FROM threads ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["id"] != "a" || rows[0]["n"] != float64(1) || rows[0]["ratio"] != 0.5 || rows[0]["note"] != nil || rows[0]["blobby"] != "hi" {
		t.Errorf("first row = %v", rows[0])
	}
	if rows[1]["note"] != "two" || rows[1]["blobby"] != nil {
		t.Errorf("second row = %v", rows[1])
	}
	// Keys keep column order, which decoding into a map cannot show.
	if !strings.HasPrefix(string(out), `[{"id":"a","n":1,"ratio":0.5,"note":null,"blobby":"hi"}`) {
		t.Errorf("column order lost: %s", out)
	}
}

func TestRunInProcessNoRowsIsEmptyOutput(t *testing.T) {
	out, err := RunInProcess(context.Background(), "-readonly", "-json", fixture(t), "SELECT id FROM threads WHERE n > 99")
	if err != nil || len(out) != 0 {
		t.Fatalf("out = %q, err = %v; want no output and no error", out, err)
	}
}

func TestRunInProcessIsReadOnly(t *testing.T) {
	path := fixture(t)
	_, err := RunInProcess(context.Background(), "-readonly", "-json", path, "DELETE FROM threads")
	if err == nil {
		t.Fatal("a write succeeded through a read-only run")
	}
	out, err := RunInProcess(context.Background(), "-readonly", "-json", path, "SELECT COUNT(*) AS c FROM threads")
	if err != nil || !strings.Contains(string(out), `"c":2`) {
		t.Fatalf("rows were changed: %q, %v", out, err)
	}
}

func TestRunInProcessErrorsDoNotLeakQueryOrPath(t *testing.T) {
	path := fixture(t)
	_, err := RunInProcess(context.Background(), "-readonly", "-json", path, "SELECT secret_column FROM nowhere")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"secret_column", "nowhere", filepath.Dir(path)} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}
	_, err = RunInProcess(context.Background(), "-readonly", "-json", filepath.Join(t.TempDir(), "missing.db"), "SELECT 1")
	if err == nil || strings.Contains(err.Error(), "missing.db") {
		t.Errorf("missing database error = %v", err)
	}
}

func TestRunInProcessErrorNamesResultCode(t *testing.T) {
	_, err := RunInProcess(context.Background(), "-readonly", "-json", fixture(t), "SELECT x FROM nowhere")
	if err == nil || !strings.Contains(err.Error(), "SQLITE_ERROR") {
		t.Errorf("error = %v, want result code SQLITE_ERROR", err)
	}
}

func TestRunInProcessRejectsOtherArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-json", "x.db", "SELECT 1"},
		{"-readonly", "-json", "x.db"},
		{"-readonly", "-csv", "x.db", "SELECT 1"},
		{"x.db", "SELECT 1", ".shell id"},
	} {
		if _, err := RunInProcess(context.Background(), args...); !errors.Is(err, ErrArguments) {
			t.Errorf("args %q: err = %v, want ErrArguments", args, err)
		}
	}
}

func TestRunFallsBackWithoutSQLite3(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out, err := Run(context.Background(), "-readonly", "-json", fixture(t), "SELECT n FROM threads ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"n":1`) || !strings.Contains(string(out), `"n":2`) {
		t.Fatalf("out = %s", out)
	}
}

func TestRunInProcessHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunInProcess(ctx, "-readonly", "-json", fixture(t), "SELECT n FROM threads")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReadOnlyDSNWindowsDrivePath(t *testing.T) {
	// filepath.Abs leaves a drive path alone only on Windows, so check the
	// slash handling directly.
	got := readOnlyDSN(`C:\Users\me\a b.db`)
	if filepath.Separator == '\\' && !strings.HasPrefix(got, "file:///C:/Users/me/a%20b.db?") {
		t.Errorf("dsn = %q", got)
	}
	if !strings.Contains(got, "mode=ro") {
		t.Errorf("dsn %q is not read-only", got)
	}
}
