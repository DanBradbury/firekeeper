package antigravityusage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Synthetic protobuf fixture layout verified against
// ayagmar/llm-usage-metrics packages/core/tests/helpers/antigravity-fixtures.ts.
func varintField(field, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, field<<3), value)
}

func bytesField(field uint64, value []byte) []byte {
	data := binary.AppendUvarint(nil, field<<3|2)
	data = binary.AppendUvarint(data, uint64(len(value)))
	return append(data, value...)
}

func usageFixture(total bool, response string) []byte {
	usage := varintField(1, 999) // Model enum, not input usage.
	usage = append(usage, varintField(2, 100)...)
	usage = append(usage, varintField(5, 20)...)
	usage = append(usage, varintField(9, 10)...)
	usage = append(usage, varintField(10, 5)...)
	if total {
		usage = append(usage, varintField(3, 15)...)
	}
	usage = append(usage, bytesField(11, []byte(response))...)
	chat := bytesField(4, usage)
	chat = append(chat, bytesField(19, []byte("gemini-test"))...)
	stamp := varintField(1, 1782510000)
	stamp = append(stamp, varintField(2, 100000000)...)
	wrapper := varintField(1, math.MaxUint64) // Unrelated opaque 64-bit ID.
	wrapper = append(wrapper, bytesField(4, stamp)...)
	chat = append(chat, bytesField(9, wrapper)...)
	return bytesField(1, chat)
}

func TestParseVerifiedUsageLayout(t *testing.T) {
	for _, total := range []bool{true, false} {
		turn, response, err := Parse(usageFixture(total, "response-a"))
		if err != nil {
			t.Fatal(err)
		}
		if turn.Input != 100 || turn.Output != 15 || turn.Cache != 20 ||
			turn.Model != "gemini-test" || turn.TS == nil ||
			turn.TS.Unix() != 1782510000 || turn.TS.Nanosecond() != 100000000 || response != "response-a" {
			t.Error("verified field layout or output/reasoning accounting differs")
		}
	}
}

func TestParseUsageCorruptionAndDrift(t *testing.T) {
	for _, blob := range [][]byte{
		nil, {0}, {0x0a, 0x80}, {0x0a, 0x10, 0x01}, {0x0b},
		bytesField(1, bytesField(4, varintField(2, math.MaxUint64))),
		bytesField(1, bytesField(4, append(varintField(9, math.MaxInt64), varintField(10, 1)...))),
		bytes.Repeat([]byte{0}, maxBlobBytes+1),
	} {
		if _, _, err := Parse(blob); err == nil {
			t.Error("malformed or oversized usage accepted")
		}
	}
	drift := append(usageFixture(true, "response-a"), bytesField(100, []byte("future field"))...)
	drift = append(drift, 0xa5, 0x06, 1, 2, 3, 4) // Unknown fixed32 field.
	if _, _, err := Parse(drift); err != nil {
		t.Error("unknown well-formed protobuf fields should remain usable")
	}
}

func createUsageDB(t *testing.T, blobs [][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB)"); err != nil {
		t.Fatal(err)
	}
	for i, blob := range blobs {
		if _, err := db.Exec("INSERT INTO gen_metadata(idx,data) VALUES(?,?)", i*2, blob); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestReadUsageReadOnlyStableRowsAndDeduplication(t *testing.T) {
	path := createUsageDB(t, [][]byte{usageFixture(true, "same"), {0x0a, 0xff}, usageFixture(true, "same"), usageFixture(false, "next")})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 2 || result.Turns[0].Seq != 0 || result.Turns[1].Seq != 3 ||
		result.Skipped != 1 || result.Input != 200 || result.Output != 30 || result.Cache != 40 {
		t.Error("usage row ordering, partial recovery, or duplicate response accounting differs")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("usage read mutated provider database")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, path); err == nil {
		t.Error("cancelled read succeeded")
	}
}

func TestReadUsageBoundsAndUnknownSchema(t *testing.T) {
	path := createUsageDB(t, [][]byte{bytes.Repeat([]byte{0}, maxBlobBytes+1)})
	if _, err := Read(context.Background(), path); err == nil {
		t.Error("oversized database payload accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE gen_metadata"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Read(context.Background(), path); err == nil {
		t.Error("unknown schema accepted")
	}
}
