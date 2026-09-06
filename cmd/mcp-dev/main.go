// Command mcp-dev serves the awg-manager MCP tool set on a developer
// machine from in-memory fake data (internal/mcp/mcptest). It exists so
// an MCP client (Claude Code, Cursor) can be pointed at the mock stack;
// it is never packaged into the IPK. Started as the fourth child of
// `npm run dev:mock:proxy` (frontend/scripts/mock-stack.mjs).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// newHandler mounts /mcp on the fake Deps. An empty key disables auth.
func newHandler(key string) http.Handler {
	server := mcp.NewServer(mcptest.New(), "dev")
	inner := mcp.NewHTTPHandler(server)
	mux := http.NewServeMux()
	if key == "" {
		mux.Handle("/mcp", inner)
	} else {
		mux.Handle("/mcp", mcp.KeyMiddleware(mcp.AuthConfig{
			Enabled: func() bool { return true },
			Verify: func(tok string) (mcp.KeyInfo, bool) {
				if tok == key {
					return mcp.KeyInfo{ID: "dev", Name: "dev"}, true
				}
				return mcp.KeyInfo{}, false
			},
			Log: log.Printf,
		}, inner))
	}
	return mux
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "address to serve /mcp on")
	key := flag.String("key", os.Getenv("MCP_DEV_KEY"), "bearer key to require; empty = no auth (dev only)")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "[mcp-dev] WARNING: no --key given, /mcp is unauthenticated")
	}
	fmt.Fprintf(os.Stderr, "[mcp-dev] serving fake awg-manager MCP at http://%s/mcp\n", *listen)
	fmt.Fprintf(os.Stderr, "[mcp-dev] connect: claude mcp add --transport http awgm-dev http://%s/mcp\n", *listen)
	if err := http.ListenAndServe(*listen, newHandler(*key)); err != nil {
		log.Fatal(err)
	}
}
