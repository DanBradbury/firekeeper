package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/DanBradbury/firekeeper/internal/iconfont"
)

// fontDir finds the user's font folder; tests replace it.
var fontDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return iconfont.UserDir(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"))
}

// ghosttyConfig returns the Ghostty config file to edit, or "" when Ghostty
// does not look like this user's terminal. Tests replace it.
var ghosttyConfig = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	path := iconfont.GhosttyConfigPath(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), exists)
	if os.Getenv("TERM_PROGRAM") == "ghostty" || exists(path) || exists("/Applications/Ghostty.app") {
		return path
	}
	return ""
}

// runInstallFont copies the embedded logo font into the user's font folder.
// It runs only when the user asks, and writes that one file.
func runInstallFont(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("firekeeper install-font", flag.ContinueOnError)
	fs.SetOutput(stderr)
	remove := fs.Bool("remove", false, "remove the installed font and the Ghostty setting instead")
	noTerminal := fs.Bool("no-terminal", false, "do not edit the Ghostty config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "firekeeper install-font: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	dir, err := fontDir()
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper install-font: %v\n", err)
		if runtime.GOOS == "windows" {
			fmt.Fprintln(stderr, "Use FIREKEEPER_ICONS=unicode, or FIREKEEPER_ICONS=off to hide the icons.")
		}
		return 1
	}
	if *remove {
		removed, err := iconfont.Remove(dir)
		if err != nil {
			fmt.Fprintf(stderr, "firekeeper install-font: %v\n", err)
			return 1
		}
		if removed {
			fmt.Fprintf(stdout, "removed %s\n", iconfont.FileName)
		} else {
			fmt.Fprintf(stdout, "%s was not installed\n", iconfont.FileName)
		}
		if path := ghosttyConfig(); path != "" && !*noTerminal {
			changed, err := iconfont.UnconfigureGhostty(path)
			if err != nil {
				fmt.Fprintf(stderr, "firekeeper install-font: %v\n", err)
				return 1
			}
			if changed {
				fmt.Fprintf(stdout, "removed the Firekeeper lines from %s\n", path)
			}
		}
		return 0
	}
	path, changed, err := iconfont.Install(dir)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper install-font: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed %s\n", path)
	} else {
		fmt.Fprintf(stdout, "%s is already installed at %s\n", iconfont.FileName, path)
	}
	if *noTerminal {
		fmt.Fprint(stdout, fontSetupHelp)
		return 0
	}
	path = ghosttyConfig()
	if path == "" {
		fmt.Fprint(stdout, fontSetupHelp)
		return 0
	}
	changed, err = iconfont.ConfigureGhostty(path)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper install-font: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "added the logo font to %s\n", path)
	} else {
		fmt.Fprintf(stdout, "%s already uses the logo font\n", path)
	}
	fmt.Fprint(stdout, "\nReload Ghostty (Cmd+Shift+, or restart it) to see the logos.\n")
	return 0
}

const fontSetupHelp = `
Next, tell your terminal to use the font, then restart it:
  Ghostty   run firekeeper install-font again from Ghostty, or add
            "font-codepoint-map = U+E9A0-U+E9A6=FirekeeperIcons" to its config
  iTerm2    Profiles > Text > "Use a different font for non-ASCII text"
  others    add FirekeeperIcons as a fallback or symbol font if the terminal has one
Terminal.app cannot use a fallback font; set FIREKEEPER_ICONS=unicode there.
Until the font is installed, the dashboard shows a plain dot instead of a logo.
`
