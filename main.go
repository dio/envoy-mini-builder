package main

import (
	"fmt"
	"os"

	"github.com/dio/envoy-mini-builder/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31m✗\033[0m %v\n", err)
		os.Exit(1)
	}
}
