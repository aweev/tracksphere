// Command migrate applies embedded SQL migrations and exits.
// Usage: migrate [--version]  (prints latest embedded version)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/db"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		v, err := db.LatestVersion()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(v)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := db.Migrate(ctx, cfg.MigrationsURL); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	latest, _ := db.LatestVersion()
	fmt.Println("migrations up to date, latest:", latest)
}