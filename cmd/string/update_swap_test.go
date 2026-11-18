package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// swapHelperEnv, when set, makes this test binary stand in for a long command
// of an old string: see TestUpdateSwapHelperProcess.
const swapHelperEnv = "STRING_TEST_SWAP_HELPER"

// TestUpdateSwapHelperProcess is the old binary the swap test replaces while
// it runs, not a test. It waits for its stdin to close, then prints what
// swapHelperEnv says.
func TestUpdateSwapHelperProcess(t *testing.T) {
	said := os.Getenv(swapHelperEnv)
	if said == "" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Stdout.WriteString(said)
	os.Exit(0)
}

// Windows refuses to replace or delete a program while it runs, and string
// update replaces the program that is running it. The old binary has to keep
// running, and the next update has to delete it once it has stopped.
func TestUpdateSwapReplacesABinaryWhileItRuns(t *testing.T) {
	test, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, binaryName())
	copyExecutable(t, test, path)
	running := exec.Command(path, "-test.run=^TestUpdateSwapHelperProcess$")
	running.Env = append(os.Environ(), swapHelperEnv+"=still running")
	stdin, err := running.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	running.Stdout = &stdout
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = running.Process.Kill()
		_ = running.Wait()
	})

	replacement := filepath.Join(dir, binaryTempPrefix+"test"+filepath.Ext(path))
	if err := os.WriteFile(replacement, []byte("string 0.3.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(replacement, path); err != nil {
		t.Fatal(err)
	}
	// The next update starts while the old binary still runs.
	removeAsides(path)
	if data, err := os.ReadFile(path); err != nil || string(data) != "string 0.3.1\n" {
		t.Fatalf("after the swap the binary holds %.20q: %v", data, err)
	}

	stdin.Close()
	if err := running.Wait(); err != nil || stdout.String() != "still running" {
		t.Fatalf("the old binary did not run on to the end: %v, printed %q", err, stdout.String())
	}
	// Windows may hold the file for a moment after the program ends.
	deadline := time.Now().Add(30 * time.Second)
	for {
		removeAsides(path)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if slices.Equal(names, []string{binaryName()}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an update after the old binary stopped left %v", names)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// When the new binary cannot be put in place, the old one stays at its path
// under its own name.
func TestUpdateSwapKeepsTheOldBinaryWhenTheNewOneCannotMoveIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, binaryName())
	if err := os.WriteFile(path, []byte("string 0.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(filepath.Join(dir, "missing"), path); err == nil {
		t.Fatal("a swap from a file that does not exist succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if data, err := os.ReadFile(path); err != nil || string(data) != "string 0.3.0\n" || len(entries) != 1 {
		t.Errorf("the binary holds %q (%v), and the directory %v", data, err, entries)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
