// Command pano is the all-seeing HTTPS proxy for AI agents.
package main

import (
	"github.com/orron/pano/internal/cli"
)

func main() {
	cli.Execute(hooks())
}
