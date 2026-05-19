package main

import (
	"os"

	"github.com/dio/envoy-mini-builder/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
