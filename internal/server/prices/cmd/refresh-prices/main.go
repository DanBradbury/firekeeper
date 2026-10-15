// Command refresh-prices rereads the default price table's rates from the
// providers' pricing pages and rewrites the table in place. The scheduled
// "Refresh default prices" workflow runs it; see docs/prices.md.
//
// Usage:
//
//	go run ./internal/server/prices/cmd/refresh-prices [-table PATH] [-report PATH] [-github-output PATH]
//
// It exits 0 after a refresh, whether or not anything changed, and 1 when
// any model could not be confirmed, leaving the table untouched. With
// -github-output it appends changed=true|false and review=true|false.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/prices/refresh"
)

// maxPage bounds a pricing page download.
const maxPage = 8 << 20

func main() {
	table := flag.String("table", "internal/server/prices/defaults.toml", "price table to refresh")
	report := flag.String("report", "", "write the pull request body here")
	ghOut := flag.String("github-output", "", "append changed= and review= outputs here ($GITHUB_OUTPUT)")
	flag.Parse()
	if err := run(*table, *report, *ghOut); err != nil {
		fmt.Fprintf(os.Stderr, "refresh-prices: %v\n", err)
		os.Exit(1)
	}
}

func run(path, reportPath, ghOut string) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, rep, err := refresh.Refresh(ctx, old, fetch, time.Now())
	if err != nil {
		return err
	}
	if rep.Changed() {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
	}
	fmt.Print(rep.Markdown())
	if reportPath != "" {
		if err := os.WriteFile(reportPath, []byte(rep.Markdown()), 0o644); err != nil {
			return err
		}
	}
	if ghOut != "" {
		f, err := os.OpenFile(ghOut, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "changed=%t\nreview=%t\n", rep.Changed(), len(rep.Review) > 0)
		return errors.Join(err, f.Close())
	}
	return nil
}

var client = &http.Client{Timeout: 30 * time.Second}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "firekeeper-refresh-prices (+https://github.com/DanBradbury/firekeeper)")
	req.Header.Set("Accept", "text/markdown, text/plain;q=0.9")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPage+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPage {
		return nil, fmt.Errorf("page larger than %d bytes", maxPage)
	}
	return body, nil
}
