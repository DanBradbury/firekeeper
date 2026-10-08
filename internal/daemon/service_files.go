package daemon

import (
	"bytes"
	"encoding/xml"
	"slices"
	"strings"
)

// launchdPlist renders the LaunchAgent. The daemon is restarted only when
// it exits with an error, so a SIGTERM from bootout stays stopped.
// ThrottleInterval spaces out restarts of a daemon that keeps failing to
// start, for example while another daemon holds the lock.
func launchdPlist(s *Service) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by firekeeper daemon install. Remove it with firekeeper daemon uninstall. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, arg := range s.programArgs() {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if keys := s.envKeys(); len(keys) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, key := range keys {
			b.WriteString("\t\t<key>" + xmlEscape(key) + "</key>\n")
			b.WriteString("\t\t<string>" + xmlEscape(s.Env[key]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	stderrLog := xmlEscape(s.StartupLogPath())
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + stderrLog + `</string>
	<key>StandardErrorPath</key>
	<string>` + stderrLog + `</string>
</dict>
</plist>
`)
	return b.Bytes()
}

// systemdUnit renders the user unit. Output goes to the user journal.
func systemdUnit(s *Service) []byte {
	var b bytes.Buffer
	b.WriteString(`# Written by firekeeper daemon install. Remove it with firekeeper daemon uninstall.
[Unit]
Description=Firekeeper daemon: uploads agent session transcripts to a Firekeeper dashboard
Documentation=https://github.com/DanBradbury/firekeeper

[Service]
Type=simple
`)
	args := s.programArgs()
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuote(arg, true)
	}
	b.WriteString("ExecStart=" + strings.Join(quoted, " ") + "\n")
	for _, key := range s.envKeys() {
		b.WriteString("Environment=" + systemdQuote(key+"="+s.Env[key], false) + "\n")
	}
	b.WriteString(`Restart=on-failure
RestartSec=30
TimeoutStopSec=60

[Install]
WantedBy=default.target
`)
	return b.Bytes()
}

func (s *Service) programArgs() []string {
	return append([]string{s.Executable, "daemon"}, s.Args...)
}

func (s *Service) envKeys() []string {
	keys := make([]string, 0, len(s.Env))
	for key := range s.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// systemdQuote double-quotes s for a unit file: backslashes and quotes are
// escaped, % is doubled so it is not read as a specifier, and in command
// lines $ is doubled so it is not expanded as a variable.
func systemdQuote(s string, command bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		case '$':
			if command {
				b.WriteString("$$")
			} else {
				b.WriteRune(r)
			}
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
