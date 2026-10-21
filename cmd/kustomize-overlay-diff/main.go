package main

import (
	"os"

	"github.com/moveeeax/kustomize-overlay-diff/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
