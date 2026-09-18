// Command local-inference-router routes OpenAI-compatible chat completions to
// local LLM inference backends.
package main

import (
	"os"

	"github.com/Yut0Miura/local-inference-router/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
