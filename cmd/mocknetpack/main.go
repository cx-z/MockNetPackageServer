// mocknetpack is the MockNetPack CLI (M12, contract v0.12.0): a machine-native
// channel for scripts and Agent tooling. See `mocknetpack --help`.
package main

import (
	"context"
	"os"

	"github.com/getmockd/mockd/pkg/mnpcli"
)

func main() {
	os.Exit(mnpcli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
