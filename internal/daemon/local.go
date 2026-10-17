package daemon

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Reporting is what this machine can tell about its own daemon without
// asking it: whether it is installed and running, where it sends sessions,
// and which providers it uploads. Every field is best effort.
type Reporting struct {
	// Installed reports whether the service definition file exists.
	Installed bool
	// Running reports whether the service is running or a daemon holds
	// the lock, such as one started in a terminal.
	Running bool
	// Foreground reports that the running daemon is not the service: the
	// service manager says it is stopped, yet a daemon holds the lock.
	Foreground bool
	// Server is the dashboard URL the daemon reports to: its --server
	// flag when the service has one, else the configured server, else
	// reporter.DefaultServer.
	Server string
	// Providers are the service's --provider flags, or the configured
	// providers when it has none.
	Providers []transcript.Provider
}

// Inspect describes the daemon. configServer and configProviders are the
// merged configuration, used for whatever the service definition does not
// set. It reads the service file and asks the service manager, so it may
// run launchctl or systemctl; it never changes anything.
func (s *Service) Inspect(ctx context.Context, configServer string, configProviders []transcript.Provider) Reporting {
	r := Reporting{Server: configServer, Providers: configProviders}
	if args, err := s.InstalledArgs(); err == nil {
		r.Installed = true
		if v := flagValues(args, "server"); len(v) > 0 {
			r.Server = v[len(v)-1]
		}
		if v := flagValues(args, "provider"); len(v) > 0 {
			r.Providers = r.Providers[:0:0]
			for _, p := range v {
				r.Providers = append(r.Providers, transcript.Provider(p))
			}
		}
	}
	if Supported(s.GOOS) {
		if st, err := s.Status(ctx); err == nil {
			r.Running = st.Running
			r.Installed = r.Installed || st.Installed
		}
	}
	if !r.Running && lockOwnerAlive(filepath.Join(s.DataDir(), "daemon.lock")) {
		r.Running, r.Foreground = true, true
	}
	if r.Server == "" {
		r.Server = reporter.DefaultServer
	}
	return r
}

// InstalledArgs returns the arguments the installed service passes after
// "daemon", read back from its definition file. It returns an error wrapping
// os.ErrNotExist when the service is not installed.
func (s *Service) InstalledArgs() ([]string, error) {
	f, err := os.Open(s.Path())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// A service file is a few kilobytes; anything far larger is not ours.
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	var args []string
	switch s.GOOS {
	case "darwin":
		args, err = plistProgramArgs(data)
	case "windows":
		args, err = taskProgramArgs(data)
	default:
		args, err = systemdExecStart(data)
	}
	if err != nil {
		return nil, err
	}
	if len(args) < 2 || args[1] != "daemon" {
		return nil, errors.New("the service definition does not run firekeeper daemon")
	}
	return args[2:], nil
}

// plistProgramArgs reads the ProgramArguments array of a LaunchAgent plist.
func plistProgramArgs(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var key string
	inArgs := false
	var args []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("read the service definition: invalid plist")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, errors.New("read the service definition: invalid plist")
				}
				key = k
			case t.Name.Local == "array" && key == "ProgramArguments" && !inArgs:
				inArgs = true
			case t.Name.Local == "string" && inArgs:
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return nil, errors.New("read the service definition: invalid plist")
				}
				args = append(args, v)
			default:
				if !inArgs {
					key = ""
				}
			}
		case xml.EndElement:
			if inArgs && t.Name.Local == "array" {
				return args, nil
			}
		}
	}
	return nil, errors.New("read the service definition: no ProgramArguments")
}

// systemdExecStart reads the ExecStart line of a unit file written by
// systemdUnit, undoing systemdQuote.
func systemdExecStart(data []byte) ([]string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		return splitSystemdWords(rest)
	}
	return nil, errors.New("read the service definition: no ExecStart")
}

func splitSystemdWords(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	inWord, quoted := false, false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quoted && r == '\\' && i+1 < len(rs):
			i++
			if rs[i] == 'n' {
				b.WriteByte('\n')
			} else {
				b.WriteRune(rs[i])
			}
		case quoted && r == '"':
			quoted = false
		case (r == '%' || r == '$') && i+1 < len(rs) && rs[i+1] == r:
			b.WriteRune(r)
			i++
		case !quoted && r == '"':
			quoted, inWord = true, true
		case !quoted && (r == ' ' || r == '\t'):
			if inWord {
				words = append(words, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteRune(r)
			inWord = true
		}
	}
	if quoted {
		return nil, errors.New("read the service definition: unterminated quote in ExecStart")
	}
	if inWord {
		words = append(words, b.String())
	}
	return words, nil
}

// flagValues returns every value given for the flag name in args, in the
// forms the flag package accepts: -name v, --name v, -name=v, --name=v.
func flagValues(args []string, name string) []string {
	var vs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		trimmed := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if trimmed == a {
			continue
		}
		if v, ok := strings.CutPrefix(trimmed, name+"="); ok {
			vs = append(vs, v)
		} else if trimmed == name && i+1 < len(args) {
			vs = append(vs, args[i+1])
			i++
		}
	}
	return vs
}
