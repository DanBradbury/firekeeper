package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// LaunchdLabel names the macOS LaunchAgent.
	LaunchdLabel = "dev.firekeeper.daemon"
	// SystemdUnit names the systemd user unit.
	SystemdUnit = "firekeeper.service"

	// stderrLogName holds what launchd captures from the daemon's stdout
	// and stderr: startup errors from before daemon.log is open. The
	// daemon itself runs with --quiet, so it does not duplicate the log.
	stderrLogName = "daemon.stderr.log"

	commandTimeout   = 30 * time.Second
	bootstrapRetries = 5
)

// serviceEnv lists the variables copied into the service definition at
// install time, when set, so the daemon sees the same provider homes and
// tools as the shell that installed it.
var serviceEnv = []string{"PATH", "CODEX_HOME", "COPILOT_HOME", "KIMI_CODE_HOME", "XDG_CONFIG_HOME"}

// goos is replaced in tests.
var goos = runtime.GOOS

// ErrNotInstalled means no service definition exists and none is loaded.
var ErrNotInstalled = errors.New("the firekeeper daemon service is not installed")

// Runner runs a service-manager command and returns its combined output.
// A command that runs and exits non-zero returns a *CommandError. Tests
// replace it so no real launchctl or systemctl runs.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// CommandError is a service-manager command that exited non-zero.
type CommandError struct {
	Name   string
	Code   int
	Output string
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("%s exited with status %d", e.Name, e.Code)
	if line := firstLine(e.Output); line != "" {
		msg += ": " + line
	}
	return msg
}

// ExecRunner runs commands with os/exec, bounded by a timeout.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, &CommandError{Name: name, Code: exitErr.ExitCode(), Output: string(out)}
	}
	if err != nil {
		return out, fmt.Errorf("run %s: %w", name, cause(err))
	}
	return out, nil
}

// Service installs, removes, and inspects the daemon as a per-user login
// service: a LaunchAgent on macOS and a systemd user unit on Linux. It
// never needs or calls sudo.
type Service struct {
	// GOOS picks the service manager: "darwin" or "linux".
	GOOS string
	// Home is the user's home directory.
	Home string
	// UID is the user's numeric id, for the launchd gui/<uid> domain.
	UID int
	// Executable is the absolute path of the firekeeper binary to run.
	Executable string
	// Args follow "daemon" on the service's command line.
	Args []string
	// Env holds the variables written into the service definition.
	Env map[string]string
	// Runner runs launchctl and systemctl.
	Runner Runner
	// Out receives progress lines.
	Out io.Writer
	// Sleep waits between bootstrap retries. Nil means time.Sleep.
	Sleep func(time.Duration)
}

// NewService returns a Service for the current user and platform, with
// the running binary as its executable and the current environment's
// provider homes and PATH captured.
func NewService(args []string, out io.Writer) (*Service, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("find home directory")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, errors.New("find the firekeeper executable")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	env := map[string]string{}
	for _, key := range serviceEnv {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	return &Service{
		GOOS:       goos,
		Home:       home,
		UID:        os.Getuid(),
		Executable: exe,
		Args:       args,
		Env:        env,
		Runner:     ExecRunner{},
		Out:        out,
	}, nil
}

// Supported reports whether goos has a service manager Service knows.
func Supported(goos string) bool {
	return goos == "darwin" || goos == "linux"
}

// Path is the service definition file.
func (s *Service) Path() string {
	if s.GOOS == "darwin" {
		return filepath.Join(s.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	}
	config := s.Env["XDG_CONFIG_HOME"]
	if !filepath.IsAbs(config) {
		config = filepath.Join(s.Home, ".config")
	}
	return filepath.Join(config, "systemd", "user", SystemdUnit)
}

// DataDir is ~/.firekeeper.
func (s *Service) DataDir() string {
	return filepath.Join(s.Home, ".firekeeper")
}

// LogPath is the daemon's own log file.
func (s *Service) LogPath() string {
	return filepath.Join(s.DataDir(), "daemon.log")
}

// StartupLogPath is where launchd writes the daemon's stdout and stderr.
func (s *Service) StartupLogPath() string {
	return filepath.Join(s.DataDir(), stderrLogName)
}

// File returns the service definition Install would write.
func (s *Service) File() ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if s.GOOS == "darwin" {
		return launchdPlist(s), nil
	}
	return systemdUnit(s), nil
}

func (s *Service) check() error {
	if !Supported(s.GOOS) {
		return fmt.Errorf("installing the daemon as a service is not supported on %s", s.GOOS)
	}
	if !filepath.IsAbs(s.Executable) {
		return errors.New("the firekeeper executable path is not absolute")
	}
	if transientExecutable(s.Executable) {
		return errors.New("firekeeper is running from a temporary build (go run?); install it first, for example with go install github.com/DanBradbury/firekeeper@latest, and run that binary")
	}
	return nil
}

// transientExecutable reports whether path looks like a go run build,
// which is deleted when go run exits.
func transientExecutable(path string) bool {
	if strings.Contains(path, string(filepath.Separator)+"go-build") {
		return true
	}
	tmp := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	return strings.HasPrefix(path, tmp)
}

// Commands lists the service-manager commands Install runs after writing
// the file, for a dry run.
func (s *Service) Commands() [][]string {
	if s.GOOS == "darwin" {
		return [][]string{
			{"launchctl", "bootout", s.target()},
			{"launchctl", "enable", s.target()},
			{"launchctl", "bootstrap", s.domain(), s.Path()},
		}
	}
	return [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", SystemdUnit},
		{"systemctl", "--user", "restart", SystemdUnit},
	}
}

// Install writes the service definition and starts the service. Running it
// again replaces the definition and restarts the service with the new one.
func (s *Service) Install(ctx context.Context) error {
	content, err := s.File()
	if err != nil {
		return err
	}
	// launchd opens StandardErrorPath without creating its directory.
	if err := os.MkdirAll(s.DataDir(), 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", cause(err))
	}
	if err := writeFileAtomic(s.Path(), content, 0o644); err != nil {
		return err
	}
	s.printf("wrote %s\n", s.Path())
	if s.GOOS == "darwin" {
		err = s.installLaunchd(ctx)
	} else {
		err = s.installSystemd(ctx)
	}
	if err != nil {
		return err
	}
	s.printf("started; it will start again at login. Check it with firekeeper daemon status and firekeeper daemon logs.\n")
	return nil
}

func (s *Service) installLaunchd(ctx context.Context) error {
	// bootstrap fails on a loaded service, so unload any earlier install.
	if s.launchdLoaded(ctx) {
		if err := s.run(ctx, "launchctl", "bootout", s.target()); err != nil {
			return err
		}
	}
	// bootstrap also fails on a service someone disabled.
	if err := s.run(ctx, "launchctl", "enable", s.target()); err != nil {
		return err
	}
	// bootout returns before launchd has finished tearing the job down, and
	// an early bootstrap fails with an I/O error, so retry briefly.
	var err error
	for attempt := 1; attempt <= bootstrapRetries; attempt++ {
		if err = s.run(ctx, "launchctl", "bootstrap", s.domain(), s.Path()); err == nil {
			return nil
		}
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) || attempt == bootstrapRetries {
			break
		}
		s.sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	return err
}

func (s *Service) installSystemd(ctx context.Context) error {
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", SystemdUnit},
		// restart also starts a stopped unit, and picks up new flags on a
		// reinstall.
		{"--user", "restart", SystemdUnit},
	} {
		if err := s.run(ctx, "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall stops the service and removes its definition. With purge it
// also deletes the reporter's and daemon's files in ~/.firekeeper. It
// returns ErrNotInstalled when there was nothing to stop or remove, after
// purging if asked.
func (s *Service) Uninstall(ctx context.Context, purge bool) error {
	if !Supported(s.GOOS) {
		return fmt.Errorf("the daemon service is not supported on %s", s.GOOS)
	}
	_, statErr := os.Stat(s.Path())
	fileExists := statErr == nil
	removed := false
	if s.GOOS == "darwin" {
		if s.launchdLoaded(ctx) {
			if err := s.run(ctx, "launchctl", "bootout", s.target()); err != nil {
				return err
			}
			s.printf("stopped %s\n", LaunchdLabel)
			removed = true
		}
	} else if fileExists {
		if err := s.run(ctx, "systemctl", "--user", "disable", "--now", SystemdUnit); err != nil {
			return err
		}
		s.printf("stopped %s\n", SystemdUnit)
	}
	if fileExists {
		if err := os.Remove(s.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove service file: %w", cause(err))
		}
		s.printf("removed %s\n", s.Path())
		removed = true
		if s.GOOS == "linux" {
			if err := s.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
				return err
			}
		}
	}
	if purge {
		if err := s.purge(); err != nil {
			return err
		}
	} else if removed {
		s.printf("left %s in place; pass --purge to delete it\n", s.DataDir())
	}
	if !removed {
		return ErrNotInstalled
	}
	return nil
}

// purgeNames are the files in ~/.firekeeper that report and daemon write.
// The serve dashboard's database is deliberately not listed.
var purgeNames = []string{
	"state.json", "machine-id", "daemon.lock", stderrLogName,
	"daemon.log", "daemon.log.1", "daemon.log.2", "daemon.log.3",
}

// purgePatterns match temporary files left by an interrupted atomic write.
var purgePatterns = []string{".state-*.json", ".machine-id-*"}

func (s *Service) purge() error {
	dir := s.DataDir()
	var paths []string
	for _, name := range purgeNames {
		paths = append(paths, filepath.Join(dir, name))
	}
	for _, pattern := range purgePatterns {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		paths = append(paths, matches...)
	}
	deleted := 0
	for _, path := range paths {
		err := os.Remove(path)
		if err == nil {
			deleted++
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("purge %s: %w", filepath.Base(path), cause(err))
		}
	}
	// Remove only succeeds on an empty directory, which keeps anything
	// else, such as a dashboard database, in place.
	if os.Remove(dir) == nil {
		s.printf("purged %s\n", dir)
		return nil
	}
	s.printf("purged %d %s from %s; other files, such as a dashboard database, were kept\n", deleted, plural(deleted, "file", "files"), dir)
	return nil
}

// Status describes the installed service.
type Status struct {
	// Installed reports whether the service definition file exists.
	Installed bool
	// Loaded reports whether the service manager knows the service.
	Loaded bool
	// Running reports whether the daemon process is up.
	Running bool
	// State is the manager's own word for the service's state.
	State string
	PID   int
	// LastExit is the last exit status, or empty when unknown.
	LastExit string
}

// Status asks the service manager about the service.
func (s *Service) Status(ctx context.Context) (Status, error) {
	if !Supported(s.GOOS) {
		return Status{}, fmt.Errorf("the daemon service is not supported on %s", s.GOOS)
	}
	var st Status
	if _, err := os.Stat(s.Path()); err == nil {
		st.Installed = true
	}
	if s.GOOS == "darwin" {
		out, err := s.Runner.Run(ctx, "launchctl", "print", s.target())
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		parseLaunchdPrint(string(out), &st)
		return st, nil
	}
	out, err := s.Runner.Run(ctx, "systemctl", "--user", "show", SystemdUnit,
		"--property=LoadState,ActiveState,SubState,MainPID,ExecMainStatus")
	if err != nil {
		return st, err
	}
	parseSystemdShow(string(out), &st)
	return st, nil
}

// parseLaunchdPrint reads the top-level state, pid, and last exit code
// from launchctl print. Its format is undocumented, so this is best
// effort; nested sections come after the top-level fields.
func parseLaunchdPrint(out string, st *Status) {
	st.Loaded = true
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		switch key {
		case "state":
			st.State = value
			st.Running = value == "running"
		case "pid":
			st.PID, _ = strconv.Atoi(value)
		case "last exit code":
			st.LastExit = value
		}
	}
}

func parseSystemdShow(out string, st *Status) {
	var active, sub string
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			st.Loaded = value == "loaded"
		case "ActiveState":
			active = value
		case "SubState":
			sub = value
		case "MainPID":
			st.PID, _ = strconv.Atoi(value)
		case "ExecMainStatus":
			st.LastExit = value
		}
	}
	st.State = active
	if sub != "" && sub != active {
		st.State += " (" + sub + ")"
	}
	st.Running = active == "active" && sub == "running"
}

func (s *Service) launchdLoaded(ctx context.Context) bool {
	_, err := s.Runner.Run(ctx, "launchctl", "print", s.target())
	return err == nil
}

func (s *Service) domain() string { return "gui/" + strconv.Itoa(s.UID) }
func (s *Service) target() string { return s.domain() + "/" + LaunchdLabel }

func (s *Service) run(ctx context.Context, name string, args ...string) error {
	_, err := s.Runner.Run(ctx, name, args...)
	return err
}

func (s *Service) sleep(d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (s *Service) printf(format string, args ...any) {
	if s.Out != nil {
		fmt.Fprintf(s.Out, format, args...)
	}
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create service directory: %w", cause(err))
	}
	f, err := os.CreateTemp(dir, ".firekeeper-service-*")
	if err != nil {
		return fmt.Errorf("write service file: %w", cause(err))
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, writeErr := f.Write(content)
	chmodErr := f.Chmod(mode)
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
		return fmt.Errorf("write service file: %w", cause(err))
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write service file: %w", cause(err))
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
