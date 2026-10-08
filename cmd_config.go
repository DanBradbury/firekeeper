package main

import (
	"fmt"
	"io"
	"os"

	"github.com/DanBradbury/firekeeper/internal/config"
)

// loadConfig merges defaults, the config file, and environment variables.
func loadConfig() (config.Config, error) {
	home, _ := os.UserHomeDir()
	return config.Load(os.Getenv, home)
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "show" {
		fmt.Fprintln(stderr, "usage: firekeeper config show")
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper config: %v\n", err)
		return 1
	}
	if err := cfg.Show(stdout); err != nil {
		fmt.Fprintf(stderr, "firekeeper config: %v\n", err)
		return 1
	}
	return 0
}
