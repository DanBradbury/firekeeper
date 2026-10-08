package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Synthetic secrets assembled at run time so the source never holds a
// complete token that secret scanners would flag.
var (
	awsKey    = "AKIA" + "Q3EXAMPLE7XYZ123"
	awsTemp   = "ASIA" + "Q3EXAMPLE7XYZ123"
	ghpToken  = "ghp_" + strings.Repeat("aB3dE", 7) + "x"
	ghsToken  = "ghs_" + strings.Repeat("Zy9", 12)
	githubPAT = "github_pat_" + "11ABCDEFG0123456789_" + strings.Repeat("aZ9", 20)
	skKey     = "sk-" + "proj-" + strings.Repeat("Ab1", 12)
	skAnt     = "sk-" + "ant-api03-" + strings.Repeat("xY7_", 10)
	stripeKey = "sk_" + "live_" + strings.Repeat("4eC39HqLyjWDarjtT1zdp7dc", 1)
	jwt       = "eyJhbGciOiJIUzI1NiJ9" + ".eyJzdWIiOiIxMjM0NTY3ODkwIn0" + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	pemBlock  = "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHB7MbEXAMPLE\nabc123==\n-----END RSA " + "PRIVATE KEY-----"
)

func TestStringPatterns(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		kinds Counts
	}{
		// AWS access key IDs.
		{"aws long-term", "aws_access_key_id " + awsKey + " ok", "aws_access_key_id [REDACTED:aws_access_key] ok", Counts{KindAWSAccessKey: 1}},
		{"aws temporary", "id=" + awsTemp, "id=[REDACTED:aws_access_key]", Counts{KindAWSAccessKey: 1}},
		{"aws too short", "AKIA1234567890", "AKIA1234567890", nil},
		{"aws lowercase", "akiaq3example7xyz123", "akiaq3example7xyz123", nil},
		{"aws embedded in word", "XAKIAQ3EXAMPLE7XYZ123", "XAKIAQ3EXAMPLE7XYZ123", nil},

		// GitHub tokens.
		{"github classic", "token " + ghpToken + ".", "token [REDACTED:github_token].", Counts{KindGitHubToken: 1}},
		{"github server", ghsToken, "[REDACTED:github_token]", Counts{KindGitHubToken: 1}},
		{"github fine-grained", "use " + githubPAT, "use [REDACTED:github_token]", Counts{KindGitHubToken: 1}},
		{"github prefix only", "the ghp_ prefix marks classic tokens", "the ghp_ prefix marks classic tokens", nil},
		{"github too short", "ghp_abc123", "ghp_abc123", nil},
		{"git sha", "commit 3f786850e387550fdab836ed7e6dc881de23001b", "commit 3f786850e387550fdab836ed7e6dc881de23001b", nil},

		// sk- style API keys.
		{"openai project key", "key: " + skKey, "key: [REDACTED:api_key]", Counts{KindAPIKey: 1}},
		{"anthropic key", skAnt + " end", "[REDACTED:api_key] end", Counts{KindAPIKey: 1}},
		{"stripe key", stripeKey, "[REDACTED:api_key]", Counts{KindAPIKey: 1}},
		{"sk short", "sk-1234", "sk-1234", nil},
		{"sk kebab words", "sk-learn-is-a-python-library-for-ml", "sk-learn-is-a-python-library-for-ml", nil},
		{"sk inside word", "disk-cleanup-2024-07-01-snapshot-name", "disk-cleanup-2024-07-01-snapshot-name", nil},

		// Bearer tokens.
		{"bearer header", "Authorization: Bearer abc123def456ghi789", "Authorization: Bearer [REDACTED:bearer_token]", Counts{KindBearerToken: 1}},
		{"bearer lowercase", "authorization: bearer abc123def456", "authorization: bearer [REDACTED:bearer_token]", Counts{KindBearerToken: 1}},
		{"bearer jwt", "Bearer " + jwt, "Bearer [REDACTED:jwt]", Counts{KindJWT: 1}},
		{"bearer prose", "send a Bearer token in the header", "send a Bearer token in the header", nil},
		{"bearer long word", "the bearer authentication scheme", "the bearer authentication scheme", nil},

		// PEM private keys.
		{"pem block", "key:\n" + pemBlock + "\ndone", "key:\n[REDACTED:private_key]\ndone", Counts{KindPrivateKey: 1}},
		{"pem pkcs8", "-----BEGIN " + "PRIVATE KEY-----\nMIIabc\n-----END " + "PRIVATE KEY-----", "[REDACTED:private_key]", Counts{KindPrivateKey: 1}},
		{"pem truncated", "x " + "-----BEGIN EC " + "PRIVATE KEY-----\nMHcCAQEEIabc", "x [REDACTED:private_key]", Counts{KindPrivateKey: 1}},
		{"pem two blocks", pemBlock + "\nmid\n" + pemBlock, "[REDACTED:private_key]\nmid\n[REDACTED:private_key]", Counts{KindPrivateKey: 2}},
		{"pem public key", "-----BEGIN PUBLIC KEY-----\nMIIBIjAN\n-----END PUBLIC KEY-----", "-----BEGIN PUBLIC KEY-----\nMIIBIjAN\n-----END PUBLIC KEY-----", nil},
		{"pem certificate", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", nil},

		// JWTs.
		{"jwt", "token=" + jwt, "token=[REDACTED:jwt]", Counts{KindJWT: 1}},
		{"jwt unsigned", "eyJhbGciOiJub25lIn0.eyJzdWIiOiIxMjM0In0.", "[REDACTED:jwt]", Counts{KindJWT: 1}},
		{"jwt one segment", "eyJhbGciOiJIUzI1NiJ9 alone", "eyJhbGciOiJIUzI1NiJ9 alone", nil},
		{"jwt dotted words", "eyJust.eyJoking.text", "eyJust.eyJoking.text", nil},

		// KEY=value assignments.
		{"env secret", "export DB_PASSWORD=hunter2 && run", "export DB_PASSWORD=[REDACTED:secret_assignment] && run", Counts{KindSecretAssignment: 1}},
		{"env key", "OPENAI_API_KEY=abc def", "OPENAI_API_KEY=[REDACTED:secret_assignment] def", Counts{KindSecretAssignment: 1}},
		{"lowercase key", "client_secret=s3cr3t", "client_secret=[REDACTED:secret_assignment]", Counts{KindSecretAssignment: 1}},
		{"cli flag", "tool --api-key=abcd1234 --verbose", "tool --api-key=[REDACTED:secret_assignment] --verbose", Counts{KindSecretAssignment: 1}},
		{"query string", "GET /cb?access_token=xyz789&state=1", "GET /cb?access_token=[REDACTED:secret_assignment]&state=1", Counts{KindSecretAssignment: 1}},
		{"quoted spaced", `password = "correct horse battery"`, `password = "[REDACTED:secret_assignment]"`, Counts{KindSecretAssignment: 1}},
		{"go short decl", `token := 'tok-123'`, `token := '[REDACTED:secret_assignment]'`, Counts{KindSecretAssignment: 1}},
		{"specific kind wins", "GITHUB_TOKEN=" + ghpToken, "GITHUB_TOKEN=[REDACTED:github_token]", Counts{KindGitHubToken: 1}},
		{"two assignments", "SECRET=a TOKEN=b", "SECRET=[REDACTED:secret_assignment] TOKEN=[REDACTED:secret_assignment]", Counts{KindSecretAssignment: 2}},
		{"unrelated key", "HOME=/tmp PATH=/bin", "HOME=/tmp PATH=/bin", nil},
		{"variable reference", "TOKEN=$GITHUB_TOKEN", "TOKEN=$GITHUB_TOKEN", nil},
		{"windows variable", "set API_KEY=%API_KEY%", "set API_KEY=%API_KEY%", nil},
		{"empty value", "TOKEN= next", "TOKEN= next", nil},
		{"comparison", `if token == "abc" {`, `if token == "abc" {`, nil},
		{"not equal", `token != "abc"`, `token != "abc"`, nil},
		{"spaced unquoted", "token = getToken()", "token = getToken()", nil},

		// Mixed text.
		{"no secrets", "Ran go test ./... and all 42 tests passed.", "Ran go test ./... and all 42 tests passed.", nil},
		{"empty", "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, counts := String(tt.in, Options{})
			if got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
			assertCounts(t, counts, tt.kinds)
		})
	}
}

func TestHomeDir(t *testing.T) {
	tests := []struct {
		name, home, in, want string
		n                    int
	}{
		{"path prefix", "/Users/me", "open /Users/me/src/app.go", "open ~/src/app.go", 1},
		{"bare home", "/Users/me", "cd /Users/me", "cd ~", 1},
		{"trailing slash option", "/Users/me/", "cd /Users/me/x", "cd ~/x", 1},
		{"quoted", "/home/dev", `"/home/dev/.codex"`, `"~/.codex"`, 1},
		{"file url", "/Users/me", "file:///Users/me/a", "file://~/a", 1},
		{"several", "/home/dev", "/home/dev/a:/home/dev/b", "~/a:~/b", 2},
		{"longer user", "/Users/me", "/Users/meg/src", "/Users/meg/src", 0},
		{"nested elsewhere", "/Users/me", "/mnt/backup/Users/me/x", "/mnt/backup/Users/me/x", 0},
		{"no option", "", "/Users/me/x", "/Users/me/x", 0},
		{"root home ignored", "/", "/etc/hosts", "/etc/hosts", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, counts := String(tt.in, Options{HomeDir: tt.home})
			if got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
			if counts[KindHomeDir] != tt.n {
				t.Fatalf("home_dir count = %d, want %d", counts[KindHomeDir], tt.n)
			}
		})
	}
}

func TestEvent(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tool := "shell"
	raw := `{"type":"function_call_output","cwd":"/Users/me/proj","output":{"stdout":"GH_TOKEN=` + ghpToken +
		`\nkey:\n` + strings.ReplaceAll(pemBlock, "\n", `\n`) + `","code":0},"args":["--token=abc123","<b>&"],"n":1.50}`
	in := transcript.Event{
		MachineID: "m1",
		SessionID: "s1",
		Provider:  transcript.ProviderCodex,
		Seq:       7,
		TS:        &ts,
		Role:      transcript.RoleToolResult,
		Text:      "GH_TOKEN=" + ghpToken + " in /Users/me/proj",
		ToolName:  &tool,
		Tokens:    transcript.Tokens{Input: 3},
		Raw:       json.RawMessage(raw),
	}
	original := string(in.Raw)

	out, counts := Event(in, Options{HomeDir: "/Users/me"})

	if string(in.Raw) != original {
		t.Fatal("Event modified the input Raw")
	}
	if out.Text != "GH_TOKEN=[REDACTED:github_token] in ~/proj" {
		t.Fatalf("Text = %q", out.Text)
	}
	if out.MachineID != "m1" || out.SessionID != "s1" || out.Seq != 7 || out.TS != &ts || out.ToolName != &tool || out.Tokens.Input != 3 {
		t.Fatal("Event changed fields other than Text and Raw")
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("redacted event invalid: %v", err)
	}
	wantRaw := `{"type":"function_call_output","cwd":"~/proj","output":{"stdout":"GH_TOKEN=[REDACTED:github_token]\nkey:\n[REDACTED:private_key]","code":0},"args":["--token=[REDACTED:secret_assignment]","<b>&"],"n":1.50}`
	if string(out.Raw) != wantRaw {
		t.Fatalf("Raw =\n%s\nwant\n%s", out.Raw, wantRaw)
	}
	assertCounts(t, counts, Counts{KindGitHubToken: 2, KindPrivateKey: 1, KindSecretAssignment: 1, KindHomeDir: 2})
	if counts.Total() != 6 {
		t.Fatalf("Total() = %d, want 6", counts.Total())
	}
}

func TestJSONEdgeCases(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unchanged keeps bytes", `{ "a" : [1, 2, "x\u0041"] }`, `{ "a" : [1, 2, "x\u0041"] }`},
		{"escaped slash path", `{"p":"\/Users\/me\/x"}`, `{"p":"~/x"}`},
		{"string document", `"PASSWORD=pw"`, `"PASSWORD=[REDACTED:secret_assignment]"`},
		{"object key", `{"/Users/me/a":1}`, `{"~/a":1}`},
		{"unicode kept", `{"m":"héllo ` + "\u2603" + ` TOKEN=v"}`, `{"m":"héllo ` + "\u2603" + ` TOKEN=[REDACTED:secret_assignment]"}`},
		{"invalid json as text", `{"broken": TOKEN=v`, `{"broken": TOKEN=[REDACTED:secret_assignment]`},
		{"empty", ``, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := JSON([]byte(tt.in), Options{HomeDir: "/Users/me"})
			if string(got) != tt.want {
				t.Fatalf("JSON() = %s, want %s", got, tt.want)
			}
			if json.Valid([]byte(tt.in)) && !json.Valid(got) {
				t.Fatalf("JSON() produced invalid JSON: %s", got)
			}
		})
	}
}

func TestIdempotent(t *testing.T) {
	inputs := []string{
		"export AWS_SECRET_ACCESS_KEY=abc/def+ghi " + awsKey,
		"Authorization: Bearer " + jwt,
		"Bearer abc123def456ghi789 and " + skKey + " and " + stripeKey,
		pemBlock + " GITHUB_TOKEN=" + ghpToken + " " + githubPAT,
		`password = "x y z" token := 'q' --api-key=k1`,
		"/Users/me/src TOKEN=\"[REDACTED:secret_assignment]\"",
		"-----BEGIN " + "PRIVATE KEY-----\ncut off",
	}
	opts := Options{HomeDir: "/Users/me"}
	for _, in := range inputs {
		once, first := String(in, opts)
		if first.Total() == 0 {
			t.Fatalf("expected %q to be redacted", in)
		}
		twice, second := String(once, opts)
		if twice != once {
			t.Fatalf("not idempotent:\nonce  %q\ntwice %q", once, twice)
		}
		if second.Total() != 0 {
			t.Fatalf("second pass counted %v on %q", second, once)
		}
	}

	raw := json.RawMessage(`{"a":"TOKEN=v ` + ghpToken + `","b":"/Users/me"}`)
	e, _ := Event(transcript.Event{Text: "SECRET=s", Raw: raw}, opts)
	again, counts := Event(e, opts)
	if again.Text != e.Text || string(again.Raw) != string(e.Raw) || counts.Total() != 0 {
		t.Fatalf("Event not idempotent: %q %s %v", again.Text, again.Raw, counts)
	}
}

func TestMarkersAreNotSecrets(t *testing.T) {
	for _, kind := range Kinds() {
		if got, counts := String(Marker(kind), Options{}); got != Marker(kind) || counts.Total() != 0 {
			t.Fatalf("marker %q redacted to %q", Marker(kind), got)
		}
	}
}

func assertCounts(t *testing.T, got, want Counts) {
	t.Helper()
	for _, kind := range Kinds() {
		if got[kind] != want[kind] {
			t.Fatalf("count[%s] = %d, want %d (all %v)", kind, got[kind], want[kind], got)
		}
	}
}

// typicalText approximates an agent transcript: prose, code, shell output,
// and paths, with no secrets.
func typicalText(size int) string {
	const chunk = `I'll update the parser so it handles the missing field. Running the tests now.
func parse(line []byte) (Event, error) {
	if len(line) == 0 {
		return Event{}, errEmpty
	}
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return Event{Role: "meta"}, nil
	}
	return rec.event(), nil
}
$ go test ./internal/transcript/...
ok  	github.com/DanBradbury/firekeeper/internal/transcript	0.012s
--- PASS: TestRead (0.00s)
commit 3f786850e387550fdab836ed7e6dc881de23001b on branch main; see docs/event.schema.json.
`
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(chunk)
	}
	return b.String()
}

func BenchmarkString(b *testing.B) {
	text := typicalText(1 << 20)
	opts := Options{HomeDir: "/Users/me"}
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		String(text, opts)
	}
}

func BenchmarkJSON(b *testing.B) {
	data, _ := json.Marshal(map[string]any{"type": "function_call_output", "output": typicalText(1 << 20)})
	opts := Options{HomeDir: "/Users/me"}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		JSON(data, opts)
	}
}

func TestUnquoteMatchesEncodingJSON(t *testing.T) {
	literals := []string{
		`"a\nb\tc\rd\"e\\f\/g\bh\fi"`,
		`"\u0041\u00e9\u2603"`,
		`"\ud83d\ude00 smile"`,
		`"lone \ud83d surrogate"`,
		`"\u003cb\u003e \u0026"`,
		"\"bad utf8 \xff \\n\"",
	}
	for _, literal := range literals {
		var want string
		if err := json.Unmarshal([]byte(literal), &want); err != nil {
			t.Fatalf("fixture %q: %v", literal, err)
		}
		got, ok := unquote([]byte(literal))
		if !ok || got != want {
			t.Fatalf("unquote(%q) = %q, %v; want %q", literal, got, ok, want)
		}
	}
}
