package serverinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultMaxDownload = 200 << 20 // archive size cap
	defaultMaxEntry    = 200 << 20 // size cap per tar entry
	maxSmall           = 1 << 20   // latest-release JSON and checksums.txt
)

var tagPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// Installer downloads, verifies and installs one release.
type Installer struct {
	Source Source
	// Client is the HTTP client; nil means one with a timeout. Redirects are
	// always checked, whatever the client.
	Client *http.Client
	// GOOS and GOARCH select the archive; empty means the running platform.
	GOOS, GOARCH string
	// MaxDownload and MaxEntry override the size caps (zero: 200 MB).
	MaxDownload, MaxEntry int64
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

func (in *Installer) logf(format string, a ...any) {
	if in.Log != nil {
		fmt.Fprintf(in.Log, format+"\n", a...)
	}
}

func (in *Installer) client() *http.Client {
	base := in.Client
	if base == nil {
		base = &http.Client{Timeout: 5 * time.Minute}
	}
	c := *base
	allow := in.Source.AllowLoopbackHTTP
	prev := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !urlAllowed(req.URL, allow) {
			return fmt.Errorf("refusing redirect to %s://%s", req.URL.Scheme, req.URL.Host)
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &c
}

func (in *Installer) get(ctx context.Context, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !urlAllowed(u, in.Source.AllowLoopbackHTTP) {
		return nil, fmt.Errorf("refusing to fetch %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "firekeeper-serverinstall")
	resp, err := in.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u.Host+u.Path, resp.Status)
	}
	return resp, nil
}

func (in *Installer) getSmall(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := in.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSmall+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSmall {
		return nil, errors.New("response too large")
	}
	return b, nil
}

// Latest returns the tag of the latest release, such as "v1.4.0". It is one
// small GET and downloads nothing else.
func (in *Installer) Latest(ctx context.Context) (string, error) {
	body, err := in.getSmall(ctx, in.Source.latestURL())
	if err != nil {
		return "", fmt.Errorf("look up latest release: %w", err)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || !tagPattern.MatchString(rel.TagName) {
		return "", errors.New("look up latest release: no usable tag_name in the response")
	}
	return rel.TagName, nil
}

// Install installs the latest release's server into dir (created if needed)
// and returns the installed path. On any error nothing is left behind in dir.
func (in *Installer) Install(ctx context.Context, dir string) (string, error) {
	goos, goarch := in.GOOS, in.GOARCH
	if goos == "" || goarch == "" {
		goos, goarch = runtimeGOOS, runtimeGOARCH
	}
	osName, arch, err := Platform(goos, goarch)
	if err != nil {
		return "", err
	}
	maxDL, maxEntry := in.MaxDownload, in.MaxEntry
	if maxDL <= 0 {
		maxDL = defaultMaxDownload
	}
	if maxEntry <= 0 {
		maxEntry = defaultMaxEntry
	}

	in.logf("Looking up the latest firekeeper-server release...")
	tag, err := in.Latest(ctx)
	if err != nil {
		return "", err
	}
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", BinaryName, strings.TrimPrefix(tag, "v"), osName, arch)

	sums, err := in.getSmall(ctx, in.Source.downloadURL(tag, "checksums.txt"))
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return "", err
	}

	work, err := os.MkdirTemp("", "firekeeper-install-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	in.logf("Downloading %s (%s)...", asset, tag)
	archive := filepath.Join(work, "archive.tar.gz")
	got, err := in.download(ctx, in.Source.downloadURL(tag, asset), archive, maxDL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if got != want {
		return "", fmt.Errorf("checksum mismatch for %s: refusing to install", asset)
	}
	in.logf("Checksum verified.")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := extractServer(archive, dir, maxEntry)
	if err != nil {
		return "", err
	}
	// The temp file is removed unless the rename below succeeds.
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmp)
		}
	}()
	if err := sanityCheck(ctx, tmp); err != nil {
		return "", err
	}
	final := filepath.Join(dir, BinaryName)
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	renamed = true
	in.logf("Installed %s", final)
	return final, nil
}

// download streams url into path, hashing as it goes, and returns the hex
// SHA-256. It fails when the body is larger than max.
func (in *Installer) download(ctx context.Context, rawURL, path string, max int64) (string, error) {
	resp, err := in.get(ctx, rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > max {
		return "", errors.New("download too large")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1))
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("download too large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checksumFor finds asset's SHA-256 in sha256sum-format text. A missing line,
// a malformed hash or conflicting lines are errors.
func checksumFor(sums []byte, asset string) (string, error) {
	var found string
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		h := strings.ToLower(fields[0])
		if len(h) != 64 {
			return "", fmt.Errorf("checksums.txt: malformed checksum for %s", asset)
		}
		if _, err := hex.DecodeString(h); err != nil {
			return "", fmt.Errorf("checksums.txt: malformed checksum for %s", asset)
		}
		if found != "" && found != h {
			return "", fmt.Errorf("checksums.txt: conflicting checksums for %s", asset)
		}
		found = h
	}
	if found == "" {
		return "", fmt.Errorf("checksums.txt has no checksum for %s: refusing to install", asset)
	}
	return found, nil
}

// extractServer reads the already verified archive. Every entry must be a
// regular file named exactly firekeeper-server or LICENSE, each at most once
// and at most maxEntry bytes; anything else fails the whole archive. Only
// firekeeper-server is written: to a 0755 temp file in dir, whose path is
// returned. On error no temp file remains.
func extractServer(archive, dir string, maxEntry int64) (tmpPath string, err error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	defer func() {
		if err != nil && tmpPath != "" {
			os.Remove(tmpPath)
			tmpPath = ""
		}
	}()
	seen := map[string]bool{}
	for {
		hdr, herr := tr.Next()
		if herr == io.EOF {
			break
		}
		if herr != nil {
			return tmpPath, fmt.Errorf("archive: %w", herr)
		}
		if hdr.Typeflag != tar.TypeReg {
			return tmpPath, fmt.Errorf("archive: entry %q is not a regular file", hdr.Name)
		}
		if hdr.Name != BinaryName && hdr.Name != "LICENSE" {
			return tmpPath, fmt.Errorf("archive: unexpected entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return tmpPath, fmt.Errorf("archive: duplicate entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		if hdr.Size < 0 || hdr.Size > maxEntry {
			return tmpPath, fmt.Errorf("archive: entry %q is too large", hdr.Name)
		}
		if hdr.Name == "LICENSE" {
			if _, cerr := io.Copy(io.Discard, io.LimitReader(tr, maxEntry+1)); cerr != nil {
				return tmpPath, fmt.Errorf("archive: %w", cerr)
			}
			continue
		}
		out, cerr := os.CreateTemp(dir, ".firekeeper-server-*")
		if cerr != nil {
			return tmpPath, cerr
		}
		tmpPath = out.Name()
		n, cerr := io.Copy(out, io.LimitReader(tr, maxEntry+1))
		if cerr == nil && n > maxEntry {
			cerr = fmt.Errorf("archive: entry %q is too large", hdr.Name)
		}
		if cerr == nil {
			cerr = out.Chmod(0o755)
		}
		if clerr := out.Close(); cerr == nil {
			cerr = clerr
		}
		if cerr != nil {
			return tmpPath, cerr
		}
	}
	if !seen[BinaryName] {
		return tmpPath, errors.New("archive: no firekeeper-server in it")
	}
	return tmpPath, nil
}

// sanityCheck runs `<path> version` and requires a clean exit.
func sanityCheck(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the downloaded server failed its `version` check: %w", err)
	}
	return nil
}
