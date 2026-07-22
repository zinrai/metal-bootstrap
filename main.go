// metal-bootstrap fetches files from URLs and extracts files from ISOs
// according to a YAML configuration. It is idempotent: running it
// repeatedly produces the same on-disk state.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to YAML config")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	showVersion := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *showVersion {
		printVersion()
		os.Exit(0)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	for _, t := range cfg.Targets {
		fmt.Printf("== target: %s ==\n", t.Name)

		for _, f := range t.Files {
			if err := processFile(f, *dryRun); err != nil {
				return fmt.Errorf("target %q: %w", t.Name, err)
			}
		}

		if len(t.ISO) > 0 {
			if err := processISO(t.ISO, *dryRun); err != nil {
				return fmt.Errorf("target %q: %w", t.Name, err)
			}
		}
	}

	return nil
}
