package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// roleNames lists every Role constant. TestAllRolesMatchesSource keeps it
// in step with the source, so a new constant fails until it is added here
// and to every table below.
var roleNames = map[string]Role{
	"RoleUnknown": RoleUnknown,
	"RoleSession": RoleSession,
	"RoleServer":  RoleServer,
	"RoleTool":    RoleTool,
}

// allRoles is every Role constant plus an invalid value, which every switch
// must treat like RoleUnknown.
func allRoles() []Role {
	roles := make([]Role, 0, len(roleNames)+1)
	for _, r := range roleNames {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	return append(roles, Role(99))
}

func TestAllRolesMatchesSource(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "signature.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		inRole := false
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if ident, ok := vs.Type.(*ast.Ident); ok {
				inRole = ident.Name == "Role"
			} else if vs.Type != nil || len(vs.Values) > 0 {
				inRole = false
			}
			if inRole {
				for _, name := range vs.Names {
					found[name.Name]++
				}
			}
		}
	}
	if len(found) != len(roleNames) {
		t.Fatalf("Role constants in source = %v, roleNames = %v", found, roleNames)
	}
	for name, n := range found {
		if _, ok := roleNames[name]; !ok || n != 1 {
			t.Errorf("Role constant %s: in roleNames %v, declared %d times", name, ok, n)
		}
	}
}

// Every switch on Role, over every value: happy path for session, server
// and tool; error path for RoleUnknown and Role(99), never shown or linked.
func TestRoleSwitches(t *testing.T) {
	tests := []struct {
		role                     Role
		link, explicit           bool
		emptyShown, linkedShown  bool
		codexGuesses, winGuesses bool
		name                     string
	}{
		{RoleUnknown, false, false, false, false, false, false, "unknown"},
		{RoleSession, true, true, true, true, true, true, "session"},
		{RoleServer, true, false, false, true, false, true, "server"},
		{RoleTool, false, false, false, false, false, false, "tool"},
		{Role(99), false, false, false, false, false, false, "invalid"},
	}
	if len(tests) != len(allRoles()) {
		t.Fatalf("table covers %d roles, allRoles has %d", len(tests), len(allRoles()))
	}
	for _, tt := range tests {
		if link, explicit := linkRole(tt.role); link != tt.link || explicit != tt.explicit {
			t.Errorf("linkRole(%s) = %v, %v; want %v, %v", tt.role, link, explicit, tt.link, tt.explicit)
		}
		if got := emitted(ProcessGroup{Role: tt.role}); got != tt.emptyShown {
			t.Errorf("emitted(empty %s) = %v, want %v", tt.role, got, tt.emptyShown)
		}
		if got := emitted(ProcessGroup{Role: tt.role, Sessions: []Info{{ID: "s"}}}); got != tt.linkedShown {
			t.Errorf("emitted(%s with a session) = %v, want %v", tt.role, got, tt.linkedShown)
		}
		if got := tt.role.String(); got != tt.name {
			t.Errorf("String(%d) = %q, want %q", int(tt.role), got, tt.name)
		}
		for _, listable := range []bool{true, false} {
			old := openFilesListable
			openFilesListable = listable
			groups := []ProcessGroup{{Tool: "Codex", Role: tt.role, Root: ProcessInfo{PID: 1}}}
			got := len(standalone().unmatchedCodexGroups(groups, map[int][]string{}, map[int]string{})) == 1
			openFilesListable = old
			want := tt.codexGuesses
			if !listable {
				want = tt.winGuesses
			}
			if got != want {
				t.Errorf("unmatchedCodexGroups(%s, listable=%v) includes it = %v, want %v", tt.role, listable, got, want)
			}
		}
	}
}

// Agreement: a command line gets a session id from an adapter only if that
// adapter classifies it as a session.
func TestSessionIDAgreesWithRole(t *testing.T) {
	lines := []string{
		"codex resume " + sessionUUID, "codex exec resume " + sessionUUID, "codex app-server resume " + sessionUUID,
		"claude --resume " + sessionUUID, "claude mcp serve --resume " + sessionUUID, "claude doctor --session-id " + sessionUUID,
		"opencode -s ses_ffe9a1b2c3d4Abc123XYZ789", "opencode serve -s ses_ffe9a1b2c3d4Abc123XYZ789",
		"copilot --resume=" + sessionUUID, "gemini --resume " + sessionUUID, "gemini mcp --resume " + sessionUUID,
		"cursor-agent --resume " + sessionUUID, "cursor-agent login --resume " + sessionUUID, "kimi -S " + sessionUUID,
	}
	for _, tt := range signatureCorpus {
		lines = append(lines, tt.line)
	}
	for _, line := range lines {
		inv := parseInvocation(fallbackArgs(line))
		for _, a := range adapters {
			if _, ok := a.SessionID(inv); !ok {
				continue
			}
			if role, matched := a.Signature().classify(inv); !matched || role != RoleSession {
				t.Errorf("%T.SessionID(%q) returned an id for role %s (matched %v)", a, line, role, matched)
			}
		}
	}
}
