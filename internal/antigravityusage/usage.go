// Package antigravityusage reads local CLI gen_metadata protobufs through
// read-only SQLite. It never reads credentials or makes network requests.
//
// Field numbers and fixture schema were verified against
// https://github.com/ayagmar/llm-usage-metrics/blob/master/packages/core/src/sources/antigravity/antigravity-row-parser.ts
// and packages/core/tests/helpers/antigravity-fixtures.ts in that repository.
package antigravityusage

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/sqlitejson"
)

const (
	maxRows       = 4096
	maxBlobBytes  = 64 << 10
	maxTotalBytes = 8 << 20
)

// Turn contains per-generation usage, not a cumulative session counter.
// Seq is the row ordinal in gen_metadata ORDER BY idx, including corrupt rows.
type Turn struct {
	Seq    int64
	TS     *time.Time
	Model  string
	Input  int64
	Output int64
	Cache  int64
}

type Summary struct {
	Turns   []Turn
	Model   string
	Input   int64
	Output  int64
	Cache   int64
	Skipped int
}

// Read uses the observed gen_metadata(idx INTEGER PRIMARY KEY, data BLOB)
// layout. Missing or evolving schemas return a sanitized error. Limits reject
// oversized histories rather than presenting a bounded tail as lifetime totals.
func Read(ctx context.Context, path string) (Summary, error) {
	var result Summary
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return result, errors.New("antigravity: usage database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	query := func(sql string) ([]byte, error) {
		out, err := sqlitejson.Run(ctx, "-readonly", "-json", path, sql)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("antigravity: usage database unreadable or schema unsupported")
		}
		return out, nil
	}
	data, err := query("SELECT COUNT(*) AS rows, COALESCE(SUM(length(data)),0) AS bytes, COALESCE(MAX(length(data)),0) AS largest FROM gen_metadata")
	if err != nil {
		return result, err
	}
	var limits []struct {
		Rows    int64 `json:"rows"`
		Bytes   int64 `json:"bytes"`
		Largest int64 `json:"largest"`
	}
	if json.Unmarshal(data, &limits) != nil || len(limits) != 1 {
		return result, errors.New("antigravity: invalid usage database summary")
	}
	if limits[0].Rows > maxRows || limits[0].Bytes > maxTotalBytes || limits[0].Largest > maxBlobBytes {
		return result, errors.New("antigravity: usage history exceeds read limits")
	}
	// Recheck sizes in the row query's own snapshot: the provider can append
	// between queries. CASE bounds even a newly inserted oversized blob.
	data, err = query("SELECT CASE WHEN length(data) <= 65536 AND (SELECT COALESCE(SUM(length(data)),0) FROM gen_metadata) <= 8388608 THEN hex(data) ELSE NULL END AS payload, length(data) AS bytes FROM gen_metadata ORDER BY idx LIMIT 4097")
	if err != nil {
		return result, err
	}
	var rows []struct {
		Payload *string `json:"payload"`
		Bytes   int64   `json:"bytes"`
	}
	if len(data) > 0 && json.Unmarshal(data, &rows) != nil {
		return result, errors.New("antigravity: invalid usage database rows")
	}
	if len(rows) > maxRows {
		return result, errors.New("antigravity: usage history exceeds read limits")
	}
	seen := map[string]bool{}
	var readBytes int64
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if row.Bytes > maxBlobBytes || row.Bytes > maxTotalBytes-readBytes || (row.Bytes > 0 && row.Payload == nil) {
			return Summary{}, errors.New("antigravity: usage history exceeds read limits")
		}
		readBytes += row.Bytes
		if row.Payload == nil {
			result.Skipped++
			continue
		}
		blob, err := hex.DecodeString(*row.Payload)
		if err != nil {
			result.Skipped++
			continue
		}
		turn, responseID, err := Parse(blob)
		if err != nil {
			result.Skipped++
			continue
		}
		if responseID != "" {
			if seen[responseID] {
				continue
			}
			seen[responseID] = true
		}
		if turn.Input > math.MaxInt64-result.Input || turn.Output > math.MaxInt64-result.Output ||
			turn.Cache > math.MaxInt64-result.Cache {
			return Summary{}, errors.New("antigravity: usage totals overflow")
		}
		turn.Seq = int64(i)
		result.Turns = append(result.Turns, turn)
		result.Input += turn.Input
		result.Output += turn.Output
		result.Cache += turn.Cache
		if turn.Model != "" {
			result.Model = turn.Model
		}
	}
	if result.Input > math.MaxInt64-result.Output || result.Input+result.Output > math.MaxInt64-result.Cache {
		return Summary{}, errors.New("antigravity: usage totals overflow")
	}
	return result, nil
}

type protoField struct {
	wire   uint64
	number uint64
	bytes  []byte
}

func fields(data []byte) (map[uint64]protoField, error) {
	out := map[uint64]protoField{}
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 || key>>3 >= 1<<29 {
			return nil, errors.New("antigravity: malformed usage protobuf")
		}
		data = data[n:]
		field := protoField{wire: key & 7}
		switch field.wire {
		case 0:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("antigravity: malformed usage varint")
			}
			field.number, data = value, data[n:]
		case 1, 5:
			size := 8
			if field.wire == 5 {
				size = 4
			}
			if len(data) < size {
				return nil, errors.New("antigravity: truncated usage protobuf")
			}
			data = data[size:]
		case 2:
			length, n := binary.Uvarint(data)
			if n <= 0 || length > uint64(len(data)-n) {
				return nil, errors.New("antigravity: truncated usage protobuf")
			}
			data = data[n:]
			field.bytes, data = data[:int(length)], data[int(length):]
		default:
			return nil, errors.New("antigravity: unsupported usage protobuf wire type")
		}
		if _, exists := out[key>>3]; !exists {
			out[key>>3] = field
		}
	}
	return out, nil
}

func nested(f map[uint64]protoField, key uint64) (map[uint64]protoField, error) {
	field, ok := f[key]
	if !ok || field.wire != 2 {
		return nil, errors.New("antigravity: missing usage protobuf field")
	}
	return fields(field.bytes)
}

func count(f map[uint64]protoField, key uint64) (int64, error) {
	field, ok := f[key]
	if !ok {
		return 0, nil
	}
	if field.wire != 0 || field.number > math.MaxInt64 {
		return 0, errors.New("antigravity: invalid token counter")
	}
	return int64(field.number), nil
}

func text(f map[uint64]protoField, key uint64) string {
	field := f[key]
	if field.wire != 2 || !utf8.Valid(field.bytes) {
		return ""
	}
	return string(field.bytes)
}

// Parse decodes a gen_metadata blob. Verified nesting is 1 (chat model), 4
// (usage); usage 1 is a model enum, NOT tokens. Output 3 already includes
// reasoning; only absent 3 falls back to visible 9 plus reasoning 10.
func Parse(blob []byte) (Turn, string, error) {
	var turn Turn
	if len(blob) == 0 || len(blob) > maxBlobBytes {
		return turn, "", errors.New("antigravity: invalid usage payload size")
	}
	top, err := fields(blob)
	if err != nil {
		return turn, "", err
	}
	chat, err := nested(top, 1)
	if err != nil {
		return turn, "", err
	}
	usage, err := nested(chat, 4)
	if err != nil {
		return turn, "", err
	}
	turn.Input, err = count(usage, 2)
	if err != nil {
		return Turn{}, "", err
	}
	turn.Cache, err = count(usage, 5)
	if err != nil {
		return Turn{}, "", err
	}
	turn.Output, err = count(usage, 3)
	if err != nil {
		return Turn{}, "", err
	}
	if _, present := usage[3]; !present {
		visible, err := count(usage, 9)
		if err != nil {
			return Turn{}, "", err
		}
		reasoning, err := count(usage, 10)
		if err != nil || visible > math.MaxInt64-reasoning {
			return Turn{}, "", errors.New("antigravity: invalid output token counter")
		}
		turn.Output = visible + reasoning
	}
	turn.Model = text(chat, 19)
	if wrapper, err := nested(chat, 9); err == nil {
		if stamp, err := nested(wrapper, 4); err == nil {
			seconds, err := count(stamp, 1)
			nanos, nanoErr := count(stamp, 2)
			if _, present := stamp[1]; present && err == nil && nanoErr == nil &&
				nanos < int64(time.Second) && seconds <= 253402300799 {
				ts := time.Unix(seconds, nanos).UTC()
				turn.TS = &ts
			}
		}
	}
	return turn, text(usage, 11), nil
}
