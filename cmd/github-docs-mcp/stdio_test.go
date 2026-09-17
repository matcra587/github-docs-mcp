package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStdioEOF exercises the real command in a child process so closing stdin
// cannot alter the test runner's own standard streams.
func TestStdioEOF(t *testing.T) {
	if os.Getenv("DOCS_TEST_STDIO_CHILD") == "1" {
		os.Args = []string{"github-docs-mcp", "-transport", "stdio", "-cache-dir", "", "-log-level", "debug"}

		os.Exit(run())
	}

	t.Parallel()

	for _, initialized := range []bool{false, true} {
		name := "before initialization"
		if initialized {
			name = "after initialization and tool listing"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			binary, err := os.Executable()
			require.NoError(t, err)

			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestStdioEOF$") //nolint:gosec // executes only this test binary, obtained from os.Executable

			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !slices.Contains(envKeys, key) {
					cmd.Env = append(cmd.Env, entry)
				}
			}

			cmd.Env = append(cmd.Env, "DOCS_TEST_STDIO_CHILD=1")

			var logs bytes.Buffer

			cmd.Stderr = &logs
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

			if initialized {
				enc, dec := json.NewEncoder(stdin), json.NewDecoder(stdout)
				require.NoError(t, enc.Encode(json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"eof-test","version":"1"}}}`)))

				var response struct {
					Result json.RawMessage
					Error  json.RawMessage
				}
				require.NoError(t, dec.Decode(&response))
				require.Empty(t, response.Error)
				require.Contains(t, string(response.Result), "serverInfo")
				require.NoError(t, enc.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
				require.NoError(t, enc.Encode(json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)))
				require.NoError(t, dec.Decode(&response))
				require.Empty(t, response.Error)
				require.Contains(t, string(response.Result), "get_doc")
			}

			require.NoError(t, stdin.Close())

			err = cmd.Wait()

			require.NoError(t, ctx.Err(), "stdin EOF did not stop the process")
			require.NoError(t, err, "%s", logs.String())
			require.Contains(t, logs.String(), "shutdown complete")
		})
	}
}
