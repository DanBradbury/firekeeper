package session

import "strings"

// processArgs returns the argument list for a process: from Options.Args
// when set, else from the operating system, else split from the ps command
// line. Fixture runs (Options.Run set without Options.Args) always split the
// ps line, so tests don't depend on the processes of the machine running them.
func (d *discoverer) processArgs(pid int, command string) []string {
	switch {
	case d.opts.Args != nil:
		if args, err := d.opts.Args(pid); err == nil && len(args) > 0 {
			return args
		}
	case d.opts.Run == nil:
		if args, err := readProcessArgs(pid); err == nil && len(args) > 0 {
			return args
		}
	}
	return fallbackArgs(command)
}

// fallbackArgs splits a ps command line when the real argument list can't
// be read. Windows command lines quote arguments that contain spaces and are
// split with CommandLineToArgvW's rules; ps output on macOS and Linux has no
// quoting and is split on whitespace. The choice depends only on the text, so
// classification is identical on every platform.
func fallbackArgs(command string) []string {
	if strings.ContainsRune(command, '"') {
		return windowsArgs(command)
	}
	return strings.Fields(command)
}

// windowsArgs splits a Windows command line the way CommandLineToArgvW
// does. The program name ends at the closing quote or the first space, with
// no escapes; later arguments follow the backslash and quote rules: 2n
// backslashes before a quote become n and the quote toggles quoting, 2n+1
// become n and a literal quote, and backslashes elsewhere are literal. Inside
// quotes, "" is a literal quote that also ends the quoted part.
func windowsArgs(command string) []string {
	cmd := strings.TrimLeft(command, " \t")
	if cmd == "" {
		return nil
	}
	var args []string
	if cmd[0] == '"' {
		end := strings.IndexByte(cmd[1:], '"')
		if end < 0 {
			return []string{cmd[1:]}
		}
		args = append(args, cmd[1:end+1])
		cmd = cmd[end+2:]
	} else {
		end := strings.IndexAny(cmd, " \t")
		if end < 0 {
			return []string{cmd}
		}
		args = append(args, cmd[:end])
		cmd = cmd[end:]
	}
	for {
		cmd = strings.TrimLeft(cmd, " \t")
		if cmd == "" {
			return args
		}
		var arg []byte
		arg, cmd = readWindowsArg(cmd)
		args = append(args, string(arg))
	}
}

func readWindowsArg(cmd string) ([]byte, string) {
	var b []byte
	inQuote := false
	slashes := 0
	for ; cmd != ""; cmd = cmd[1:] {
		c := cmd[0]
		switch c {
		case ' ', '\t':
			if !inQuote {
				return appendSlashes(b, slashes), cmd[1:]
			}
		case '"':
			b = appendSlashes(b, slashes/2)
			if slashes%2 == 0 {
				if inQuote && len(cmd) > 1 && cmd[1] == '"' {
					b = append(b, '"')
					cmd = cmd[1:]
				}
				inQuote = !inQuote
			} else {
				b = append(b, '"')
			}
			slashes = 0
			continue
		case '\\':
			slashes++
			continue
		}
		b = appendSlashes(b, slashes)
		slashes = 0
		b = append(b, c)
	}
	return appendSlashes(b, slashes), ""
}

func appendSlashes(b []byte, n int) []byte {
	for range n {
		b = append(b, '\\')
	}
	return b
}

// splitNULArgs splits a NUL-separated argument block, such as Linux's
// /proc/<pid>/cmdline, keeping at most limit arguments when limit >= 0.
func splitNULArgs(block []byte, limit int) []string {
	var args []string
	for len(block) > 0 && (limit < 0 || len(args) < limit) {
		end := 0
		for end < len(block) && block[end] != 0 {
			end++
		}
		args = append(args, string(block[:end]))
		if end == len(block) {
			break
		}
		block = block[end+1:]
	}
	// A trailing NUL leaves no empty last argument.
	for len(args) > 0 && limit < 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	return args
}
