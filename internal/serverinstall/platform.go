package serverinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Platform maps Go's GOOS/GOARCH to the release asset names, or says the
// platform has no server build.
func Platform(goos, goarch string) (osName, arch string, err error) {
	switch goos {
	case "darwin", "linux", "windows":
		osName = goos
	default:
		return "", "", fmt.Errorf("no firekeeper-server build for %s/%s (builds exist for darwin, linux and windows on amd64 and arm64)", goos, goarch)
	}
	switch goarch {
	case "amd64", "arm64":
		arch = goarch
	default:
		return "", "", fmt.Errorf("no firekeeper-server build for %s/%s (builds exist for darwin, linux and windows on amd64 and arm64)", goos, goarch)
	}
	return osName, arch, nil
}

// AssetName is the release archive for tag on osName/arch (as returned by
// Platform): firekeeper-server_<version>_<os>_<arch>.tar.gz, or .zip on
// Windows. version is tag without its leading "v".
func AssetName(tag, osName, arch string) string {
	ext := ".tar.gz"
	if osName == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", BinaryName, strings.TrimPrefix(tag, "v"), osName, arch, ext)
}

// ExecutableName is the server's file name on goos: firekeeper-server, or
// firekeeper-server.exe on Windows. It is also the name of the binary inside
// that platform's archive.
func ExecutableName(goos string) string {
	if goos == "windows" {
		return BinaryName + ".exe"
	}
	return BinaryName
}

// IsHomebrew reports whether path lies under a Homebrew prefix. Such a
// directory belongs to brew, so nothing else writes into it.
func IsHomebrew(path string) bool {
	p := filepath.ToSlash(path)
	for _, m := range []string{"/Cellar/", "/homebrew/", "/linuxbrew/"} {
		if strings.Contains(p, m) {
			return true
		}
	}
	return false
}

// DirWritable reports whether a file can be created in dir.
func DirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".fk-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// ChooseDir picks the install directory: the directory of the running
// executable (symlinks already resolved by the caller) if it is writable and
// not Homebrew's, else ~/.local/bin. fallback is true for the latter. The
// directory is not created here.
func ChooseDir(exePath, home string) (dir string, fallback bool, err error) {
	if exePath != "" {
		d := filepath.Dir(exePath)
		if !IsHomebrew(d) && DirWritable(d) {
			return d, false, nil
		}
	}
	if home == "" {
		return "", false, fmt.Errorf("cannot find a writable install directory: the firekeeper directory is not writable and the home directory is unknown")
	}
	return filepath.Join(home, ".local", "bin"), true, nil
}

// OnPath reports whether dir is one of the directories in pathEnv. On
// Windows the comparison ignores case, as the file system does.
func OnPath(dir, pathEnv string) bool {
	dir = filepath.Clean(dir)
	for _, p := range filepath.SplitList(pathEnv) {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if p == dir || (runtimeGOOS == "windows" && strings.EqualFold(p, dir)) {
			return true
		}
	}
	return false
}
