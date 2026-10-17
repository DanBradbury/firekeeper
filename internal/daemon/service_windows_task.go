package daemon

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// This file runs the daemon as a Windows Task Scheduler task that starts at
// logon, the Windows counterpart of a LaunchAgent or systemd user unit. It
// compiles on every platform so the task definition can be tested anywhere;
// only Service with GOOS "windows" uses it.

const (
	// TaskName names the Task Scheduler task.
	TaskName = "Firekeeper Daemon"

	// taskFileName is the task definition kept beside the daemon's other
	// files. Task Scheduler stores its own copy when the task is registered;
	// this one is what install wrote, for dry runs and for reading the
	// installed flags back.
	taskFileName = "daemon-task.xml"

	taskNamespace = "http://schemas.microsoft.com/windows/2004/02/mit/task"
)

// windowsStillActive is the task result Task Scheduler reports while an
// action runs.
const windowsStillActive = 267009

// consoleHostArgs runs the daemon without a console window. conhost.exe
// --headless is how Windows 10 1809 and later start a console program
// invisibly; a console program started from a logon task would otherwise
// leave an empty window open.
var consoleHostArgs = []string{"--headless"}

// taskXML renders the task definition as UTF-8 text. Install writes it as
// UTF-16, which Task Scheduler's import accepts everywhere.
func taskXML(s *Service) []byte {
	var b bytes.Buffer
	user := xmlEscape(s.User)
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="` + taskNamespace + `">
  <RegistrationInfo>
    <Description>Firekeeper daemon: uploads agent session transcripts to a Firekeeper dashboard. Written by firekeeper daemon install; remove it with firekeeper daemon uninstall.</Description>
    <URI>\` + xmlEscape(s.taskName()) + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
`)
	if user != "" {
		b.WriteString("      <UserId>" + user + "</UserId>\n")
	}
	b.WriteString(`    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
`)
	if user != "" {
		b.WriteString("      <UserId>" + user + "</UserId>\n")
	}
	// InteractiveToken runs as the signed-in user with no stored password,
	// and LeastPrivilege never asks for elevation.
	b.WriteString(`      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(s.consoleHost()) + `</Command>
      <Arguments>` + xmlEscape(joinWindowsArgs(append(append([]string{}, consoleHostArgs...), s.programArgs()...))) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`)
	return b.Bytes()
}

// taskName is the task's name: TaskName, unless the Service names another,
// as tests do so they never touch a real install.
func (s *Service) taskName() string {
	if s.TaskName != "" {
		return s.TaskName
	}
	return TaskName
}

// consoleHost is conhost.exe in the system directory.
func (s *Service) consoleHost() string {
	root := s.Env["SystemRoot"]
	if root == "" {
		root = `C:\Windows`
	}
	return strings.TrimRight(root, `\/`) + `\System32\conhost.exe`
}

// taskFilePath is the task definition file.
func (s *Service) taskFilePath() string {
	return filepath.Join(s.DataDir(), taskFileName)
}

// taskCommands are the schtasks commands Install runs, after the definition
// is written and any earlier run is stopped.
func (s *Service) taskCommands() [][]string {
	return [][]string{
		{"schtasks", "/Create", "/TN", s.taskName(), "/XML", s.taskFilePath(), "/F"},
		{"schtasks", "/Run", "/TN", s.taskName()},
	}
}

func (s *Service) installTask(ctx context.Context) error {
	// An earlier install may be running with old flags. /End fails when the
	// task is not running, which is the usual case.
	_ = s.run(ctx, "schtasks", "/End", "/TN", s.taskName())
	cmds := s.taskCommands()
	if err := s.run(ctx, cmds[0][0], cmds[0][1:]...); err != nil {
		return err
	}
	return s.run(ctx, cmds[1][0], cmds[1][1:]...)
}

// taskRegistered reports whether Task Scheduler knows the task. It looks
// only at schtasks' exit status, because its text is localized.
func (s *Service) taskRegistered(ctx context.Context) bool {
	_, err := s.Runner.Run(ctx, "schtasks", "/Query", "/TN", s.taskName())
	return err == nil
}

// uninstallTask stops and deletes the task. It reports whether a task was
// registered.
func (s *Service) uninstallTask(ctx context.Context) (bool, error) {
	if !s.taskRegistered(ctx) {
		return false, nil
	}
	_ = s.run(ctx, "schtasks", "/End", "/TN", s.taskName())
	if err := s.run(ctx, "schtasks", "/Delete", "/TN", s.taskName(), "/F"); err != nil {
		return true, err
	}
	s.printf("stopped and removed the %q task\n", s.taskName())
	return true, nil
}

// taskStatus asks Task Scheduler whether the task exists and the lock file
// whether the daemon is up. Task Scheduler's own state words are localized,
// so the running state and pid come from the daemon's lock instead.
func (s *Service) taskStatus(ctx context.Context, st *Status) error {
	out, err := s.Runner.Run(ctx, "schtasks", "/Query", "/TN", s.taskName(), "/FO", "CSV", "/NH", "/V")
	var cmdErr *CommandError
	switch {
	case errors.As(err, &cmdErr):
		return nil
	case err != nil:
		return err
	}
	st.Loaded = true
	st.LastExit = taskLastResult(string(out))
	pid, alive := lockPID(filepath.Join(s.DataDir(), "daemon.lock"))
	switch {
	case alive:
		st.Running, st.PID, st.State = true, pid, "running"
	default:
		st.State = "ready"
	}
	return nil
}

// taskLastResult reads the Last Result column of schtasks /Query /V /FO CSV
// /NH, which is the seventh column on every Windows version that has it.
// A task that has never run, or is running, has no exit status to show.
func taskLastResult(out string) string {
	r := csv.NewReader(strings.NewReader(strings.TrimSpace(out)))
	r.FieldsPerRecord = -1
	record, err := r.Read()
	if err != nil || len(record) < 7 {
		return ""
	}
	code, err := strconv.ParseInt(strings.TrimSpace(record[6]), 10, 64)
	if err != nil || code == windowsStillActive {
		return ""
	}
	if code < 0 || code > 0xFFFF {
		return fmt.Sprintf("0x%x", uint32(code))
	}
	return strconv.FormatInt(code, 10)
}

// lockPID reads the pid the daemon wrote into its lock file and reports
// whether that process is alive.
func lockPID(path string) (pid int, alive bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, lockOwnerAlive(path)
}

// taskProgramArgs reads the command a task definition runs, as the
// arguments of the firekeeper executable: executable first, then "daemon"
// and its flags, like the other service formats.
func taskProgramArgs(data []byte) ([]string, error) {
	text, err := decodeText(data)
	if err != nil {
		return nil, errors.New("read the service definition: invalid task file")
	}
	var task struct {
		Command   string `xml:"Actions>Exec>Command"`
		Arguments string `xml:"Actions>Exec>Arguments"`
	}
	dec := xml.NewDecoder(strings.NewReader(text))
	// The file declares UTF-16 but has been decoded already.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := dec.Decode(&task); err != nil {
		return nil, errors.New("read the service definition: invalid task file")
	}
	if strings.TrimSpace(task.Command) == "" {
		return nil, errors.New("read the service definition: no command")
	}
	args := splitWindowsCommandLine(task.Arguments)
	if strings.EqualFold(baseOf(task.Command), "conhost.exe") {
		// Drop the console host's own flags, such as --headless.
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:]
		}
	} else {
		args = append([]string{task.Command}, args...)
	}
	if len(args) == 0 {
		return nil, errors.New("read the service definition: no command")
	}
	return args, nil
}

func baseOf(path string) string {
	return path[strings.LastIndexAny(path, `/\`)+1:]
}

// joinWindowsArgs quotes args into one command line that
// CommandLineToArgvW splits back into the same arguments.
func joinWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quoteWindowsArg(a)
	}
	return strings.Join(quoted, " ")
}

// quoteWindowsArg follows the CommandLineToArgvW rules: quotes are needed
// for empty arguments and ones holding a space, tab or quote; a run of
// backslashes is doubled only when a quote follows it.
func quoteWindowsArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\n\v\"") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range a {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}

// splitWindowsCommandLine is the inverse of joinWindowsArgs.
func splitWindowsCommandLine(s string) []string {
	var args []string
	var b strings.Builder
	inWord, quoted := false, false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\\':
			n := 0
			for i < len(rs) && rs[i] == '\\' {
				n++
				i++
			}
			if i < len(rs) && rs[i] == '"' {
				b.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					b.WriteByte('"')
				} else {
					quoted = !quoted
				}
			} else {
				b.WriteString(strings.Repeat(`\`, n))
				i--
			}
			inWord = true
		case r == '"':
			quoted, inWord = !quoted, true
		case !quoted && (r == ' ' || r == '\t'):
			if inWord {
				args = append(args, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		args = append(args, b.String())
	}
	return args
}

// encodeUTF16 returns text as UTF-16 little endian with a byte order mark,
// the encoding Task Scheduler exports and imports.
func encodeUTF16(text []byte) []byte {
	units := utf16.Encode([]rune(string(text)))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// decodeText returns data as UTF-8 text, decoding UTF-16 when a byte order
// mark says it is.
func decodeText(data []byte) (string, error) {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		body := data[2:]
		if len(body)%2 != 0 {
			return "", errors.New("odd UTF-16 length")
		}
		units := make([]uint16, len(body)/2)
		for i := range units {
			units[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
		}
		return string(utf16.Decode(units)), nil
	}
	return strings.TrimPrefix(string(data), "\ufeff"), nil
}
