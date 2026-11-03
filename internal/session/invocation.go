package session

import "strings"

// launcher says how a provider's program was started: directly, or as a
// script run by an interpreter such as node.
type launcher int

const (
	// LauncherNone means the program itself is the first argument.
	LauncherNone launcher = iota
	// LauncherScript means an interpreter ran a script, which is the entry
	// point a signature matches.
	LauncherScript
)

// invocation is one process's command line, parsed into the parts a
// signature matches: the program, the entry point when an interpreter ran a
// script, and the arguments after them. The zero value has no program and
// matches no provider.
type invocation struct {
	// Args is the whole argument list as read, Args[0] being the program.
	Args []string
	// Program is the path of the program that runs, after any console host.
	Program string
	// Launcher is LauncherScript when Program is an interpreter and Script
	// is the entry point it runs.
	Launcher launcher
	// Script is the entry point path, set only for LauncherScript.
	Script string
	// Rest is everything after the program, or after the script.
	Rest []string
}

// programName returns the lowercased base name of the program.
func (inv invocation) programName() string {
	return strings.ToLower(baseName(inv.Program))
}

// interpreterValueFlags are interpreter flags whose value is the next
// argument, so that value is never mistaken for the script.
var interpreterValueFlags = map[string]bool{
	"-r": true, "--require": true, "--import": true, "--loader": true,
	"--experimental-loader": true, "-C": true, "--conditions": true,
	"--title": true, "--env-file": true, "--input-type": true,
	"--inspect-port": true, "--cpu-prof-dir": true, "--heap-prof-dir": true,
}

// interpreterInlineFlags run code given on the command line instead of a
// script (node -e, node -p, sh -c, python -c and python -m). A command line
// using one has no entry point, so it matches no provider.
var interpreterInlineFlags = map[string]bool{
	"-e": true, "--eval": true, "-p": true, "--print": true, "-c": true,
	"-m": true,
}

// interpreters are the programs that run a provider as a script.
var interpreters = map[string]bool{
	"node": true, "node.exe": true, "nodejs": true, "bun": true, "bun.exe": true,
	"bash": true, "sh": true, "zsh": true,
	"python": true, "python3": true, "python.exe": true, "python3.exe": true,
}

// parseInvocation splits an argument list into program, launcher, script and
// the remaining arguments. A Windows console host (conhost.exe) is skipped,
// with its flags, so the program it hosts is the one classified. An
// interpreter's own flags are skipped up to the script; inline code (-e, -p,
// -c, -m, --eval) before the script leaves no entry point. Flags after the
// script belong to the provider.
func parseInvocation(args []string) invocation {
	inv := invocation{Args: args}
	if len(args) == 0 || args[0] == "" {
		return inv
	}
	rest := args
	if strings.EqualFold(baseName(rest[0]), "conhost.exe") {
		rest = rest[1:]
		for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || strings.HasPrefix(strings.ToLower(rest[0]), "0x")) {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return inv
		}
	}
	inv.Program = rest[0]
	rest = rest[1:]
	if !interpreters[inv.programName()] {
		inv.Rest = rest
		return inv
	}
	inv.Launcher = LauncherScript
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--" {
			if i+1 < len(rest) {
				inv.Script = rest[i+1]
				inv.Rest = rest[i+2:]
			}
			return inv
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			inv.Script = arg
			inv.Rest = rest[i+1:]
			return inv
		}
		if interpreterInlineFlags[arg] {
			return inv // inline code: no entry point
		}
		if interpreterValueFlags[arg] {
			i++ // the flag's value
		}
	}
	return inv
}

// flagSpec describes a provider's own flags, so the scan for a subcommand
// never reads a flag's value as the subcommand.
type flagSpec struct {
	// value flags take the next argument, unless written --flag=value.
	value map[string]bool
	// optional flags take the next argument when it doesn't start with "-".
	optional map[string]bool
	// multi flags take every following argument up to the next flag.
	multi map[string]bool
	// boolean flags take no value.
	boolean map[string]bool
}

func (s flagSpec) known(flag string) bool {
	return s.value[flag] || s.optional[flag] || s.multi[flag] || s.boolean[flag]
}

// shortAttached reports whether arg is a known short value flag with its
// value attached, such as -mkimi-k2.
func (s flagSpec) shortAttached(arg string) bool {
	if len(arg) <= 2 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	short := arg[:2]
	return s.value[short] || s.optional[short] || s.multi[short]
}

// commandWords is the result of scanning a provider's arguments for its
// subcommand.
type commandWords struct {
	// Words are up to the first two words that aren't flags or flag values,
	// in order: the candidate subcommand path. Empty when there is none.
	Words []string
	// Index is the position in the scanned arguments of Words[0], or -1.
	Index int
	// Uncertain is set when an unknown flag, written as a separate word, was
	// followed by a word before the subcommand: that word may have been the
	// flag's value, so the subcommand can't be known. Callers then use the
	// signature's default role.
	Uncertain bool
}

// scanCommand reads args once, left to right, skipping flags and their
// values, and returns the candidate subcommand path. See commandWords for the
// uncertain case. "--" ends the scan with no subcommand.
func scanCommand(args []string, spec flagSpec) commandWords {
	result := commandWords{Index: -1}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return result
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			result.Index = i
			result.Words = []string{arg}
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				result.Words = append(result.Words, args[i+1])
			}
			return result
		case strings.Contains(arg, "="):
			// --flag=value: the value is attached, known flag or not.
		case spec.value[arg]:
			// The next argument is the value, even one starting with "-"
			// (kimi -p "-S abc"). But when such a value is followed by a
			// word, it may instead be a flag of its own with the value
			// missing, so the word's role is uncertain.
			if i+2 < len(args) && strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+2], "-") {
				result.Uncertain = true
			}
			i++
		case spec.optional[arg]:
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case spec.multi[arg]:
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case spec.boolean[arg], spec.shortAttached(arg):
		default:
			// An unknown flag: if a word follows, it may be this flag's value.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				result.Uncertain = true
			}
		}
	}
	return result
}

// flagValue returns the value given for any of flags in args, written as
// --flag value or --flag=value, scanning only up to "--". It reports false
// when no flag is present or a separate flag has no value after it.
func flagValue(args []string, flags ...string) (string, bool) {
	for i, arg := range args {
		if arg == "--" {
			return "", false
		}
		for _, flag := range flags {
			if value, ok := strings.CutPrefix(arg, flag+"="); ok {
				return value, true
			}
			if arg == flag && i+1 < len(args) {
				return args[i+1], true
			}
		}
	}
	return "", false
}

// matchCommand returns the longest known subcommand path in args, one or
// two words such as "web" or "web rotate-token", as known reports them. It
// reports uncertain, with no path, when the scan is uncertain, or when a
// known second word appears later than right after the first word, past
// flags it can't be placed against (kimi web --foo rotate-token). A first
// word that isn't a known path, such as a prompt, gives no path and is not
// uncertain: the signature's default role applies either way.
func matchCommand(args []string, spec flagSpec, known func(path string) bool) (path string, uncertain bool) {
	cw := scanCommand(args, spec)
	if cw.Uncertain {
		return "", true
	}
	if len(cw.Words) == 0 {
		return "", false
	}
	first := cw.Words[0]
	if len(cw.Words) == 2 && known(first+" "+cw.Words[1]) {
		return first + " " + cw.Words[1], false
	}
	for _, later := range args[cw.Index+1:] {
		if later == "--" {
			break
		}
		if !strings.HasPrefix(later, "-") && known(first+" "+later) {
			return "", true
		}
	}
	if known(first) {
		return first, false
	}
	return "", false
}

// hasFlag reports whether any of names appears among args as a flag, before
// "--", skipping the values of the spec's value flags so a prompt such as
// kimi -p "-c" doesn't count.
func hasFlag(args []string, spec flagSpec, names ...string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		for _, name := range names {
			if arg == name {
				return true
			}
		}
		if spec.value[arg] {
			i++
		}
	}
	return false
}
