package docs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiskCacheFiveProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	executable, err := os.Executable()
	require.NoError(t, err)

	var (
		commands []*exec.Cmd
		gates    []io.WriteCloser
		outputs  []*bytes.Buffer
		results  []<-chan processOutput
	)

	for i := range 5 {
		// #nosec G204 -- Re-executes this test binary, not user input.
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDiskCacheProcessHelper$")

		cmd.Env = append(os.Environ(), "DOCS_DISK_TEST_DIR="+dir, "DOCS_DISK_TEST_WRITER="+strconv.Itoa(i))
		gate, err := cmd.StdinPipe()
		require.NoError(t, err)
		ready, err := cmd.StdoutPipe()
		require.NoError(t, err)

		output := new(bytes.Buffer)
		cmd.Stderr = output
		require.NoError(t, cmd.Start())
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})

		commands = append(commands, cmd)
		gates = append(gates, gate)
		outputs = append(outputs, output)
		reader := bufio.NewReader(ready)
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "ready\n", line)

		result := make(chan processOutput, 1)
		results = append(results, result)

		go func() {
			output, err := io.ReadAll(reader)
			result <- processOutput{text: string(output), err: err}
		}()
	}

	for _, gate := range gates {
		require.NoError(t, gate.Close())
	}

	for i, cmd := range commands {
		result := <-results[i]
		require.NoError(t, cmd.Wait(), result.text+outputs[i].String())
		require.NoError(t, result.err)
	}

	d, err := NewDiskCache(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.root.Close() })

	entries, err := d.Load()
	require.NoError(t, err)
	// Every writer contends on the catalogue keys while forcing page eviction.
	require.Len(t, entries, 4)

	keys := make(map[string]bool)
	for _, entry := range entries {
		keys[entry.Key] = true
	}

	require.True(t, keys[diskIndexKey])
	require.True(t, keys[diskPageListKey])
}

type processOutput struct {
	text string
	err  error
}

func TestDiskCacheProcessHelper(t *testing.T) {
	t.Parallel()

	dir := os.Getenv("DOCS_DISK_TEST_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}

	d, err := NewDiskCache(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.root.Close() })

	d.maxBytes = 4 * 4096

	writer := os.Getenv("DOCS_DISK_TEST_WRITER")
	if writer == "hold" {
		lock, err := d.lock()
		require.NoError(t, err)

		defer func() { _ = lock.Close() }()

		_, err = fmt.Fprintln(os.Stdout, "ready")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)

		return
	}

	payload := bytes.Repeat([]byte(writer), 4096)
	_, err = fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, os.Stdin)
	require.NoError(t, err)

	for i := range 30 {
		for _, key := range []string{diskIndexKey, diskPageListKey, fmt.Sprintf("page-%s-%d", writer, i)} {
			require.NoError(t, d.Store(key, payload))
		}

		entries, err := d.Load()
		require.NoError(t, err)
		require.LessOrEqual(t, len(entries), 4)

		for _, entry := range entries {
			require.Len(t, entry.Value, 4096)
			require.Contains(t, "01234", string(entry.Value[0]))
			require.Equal(t, bytes.Repeat(entry.Value[:1], 4096), entry.Value)
		}
		// Inspect physical files too: Load's own budget could hide excess storage.
		lock, err := d.lock()
		require.NoError(t, err)
		files, err := os.ReadDir(dir)
		require.NoError(t, err)

		var total int64

		for _, file := range files {
			if file.Name() == diskLockName {
				continue
			}

			info, err := file.Info()
			require.NoError(t, err)

			total += info.Size()
		}

		require.NoError(t, lock.Close())
		require.LessOrEqual(t, total, d.maxBytes)

		if i > 0 {
			require.Equal(t, d.maxBytes, total, "concurrent pruning over-evicted")
		}
	}
}

func TestDiskCacheLockReleasedOnExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	executable, err := os.Executable()
	require.NoError(t, err)
	// #nosec G204 -- Re-executes this test binary, not user input.
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDiskCacheProcessHelper$")

	cmd.Env = append(os.Environ(), "DOCS_DISK_TEST_DIR="+dir, "DOCS_DISK_TEST_WRITER=hold")
	gate, err := cmd.StdinPipe()
	require.NoError(t, err)

	defer func() { _ = gate.Close() }()

	ready, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	line, err := bufio.NewReader(ready).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)

	d, err := NewDiskCache(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.root.Close() })
	require.ErrorIs(t, d.Store("key", []byte("before")), os.ErrDeadlineExceeded)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NoError(t, d.Store("key", []byte("after")))
	entries, err := d.Load()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "after", string(entries[0].Value))
}

func TestDiskCacheLockContention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewDiskCache(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.root.Close() })

	b, err := NewDiskCache(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.root.Close() })

	lock, err := a.lock()
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	require.ErrorIs(t, b.Store("key", []byte("value")), os.ErrDeadlineExceeded)
	_, err = b.Load()
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.NoError(t, lock.Close())
	require.NoError(t, b.Store("key", []byte("value")))
	entries, err := b.Load()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "value", string(entries[0].Value))

	files, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, file := range files {
		require.NotContains(t, file.Name(), ".tmp.")
	}
}
