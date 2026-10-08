//go:build darwin || linux

package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const interruptionOptionsEnv = "LEVARA_BACKUP_INTERRUPTION_OPTIONS"

// This helper performs the real backup, then stops the whole process after its
// monitor observes actual archive output. No synthetic archive/stage is seeded.
func TestVerifiedBackupInterruptionProcessHelper(t *testing.T) {
	optionsFile := os.Getenv(interruptionOptionsEnv)
	if optionsFile == "" {
		return
	}
	body, err := os.ReadFile(optionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var options VerifiedOptions
	if err := json.Unmarshal(body, &options); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go func() {
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				entries, err := os.ReadDir(options.OutputDir)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					if !strings.HasPrefix(entry.Name(), ".verified-archive-") || !strings.HasSuffix(entry.Name(), ".tar.gz") {
						continue
					}
					info, err := entry.Info()
					if err != nil || info.Size() == 0 {
						continue
					}
					_, _ = fmt.Fprintln(os.Stdout, "archive-ready")
					// Parent also signals STOP before inspecting the state, closing the tiny
					// scheduling gap between readiness and this process-wide stop.
					if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
						os.Exit(3)
					}
					return
				}
			}
		}
	}()
	if _, err := CreateVerifiedBackup(ctx, options); err != nil {
		t.Fatal(err)
	}
	t.Fatal("backup completed before the interruption barrier")
}

func TestVerifiedInterruptedProcessPreservesPublishedBackup(t *testing.T) {
	options := verifiedFixture(t)
	first, err := CreateVerifiedBackup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(options.OutputDir, "last_successful_restore.json")
	receiptBefore, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	archiveBefore, err := os.ReadFile(first.Archive)
	if err != nil {
		t.Fatal(err)
	}
	hashBefore := sha256.Sum256(archiveBefore)
	// Incompressible bytes keep the real gzip write open long enough for the
	// observation barrier; size remains bounded and well below archive budgets.
	const payloadSize = 8 << 20
	payload := make([]byte, payloadSize)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	writeVerifiedFile(t, filepath.Join(options.DataDir, "interrupt-payload.bin"), payload)
	optionsBody, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	optionsFile := filepath.Join(t.TempDir(), "options.json")
	writeVerifiedFile(t, optionsFile, optionsBody)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVerifiedBackupInterruptionProcessHelper$")
	command.Env = append(os.Environ(), interruptionOptionsEnv+"="+optionsFile, "GOMAXPROCS=2")
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "archive-ready\n" {
			err = fmt.Errorf("unexpected helper readiness %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("helper did not reach archive write: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out observing real archive write")
	}
	if err := command.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// STOP is asynchronous, so inspect only after the kernel reports a stopped
	// child. WUNTRACED consumes the stop notification, not its eventual exit.
	stopped := make(chan error, 1)
	go func() {
		var state syscall.WaitStatus
		pid, err := syscall.Wait4(command.Process.Pid, &state, syscall.WUNTRACED, nil)
		// BSD's Go helper classifies a SIGSTOP status as Continued. Verify the
		// native stopped encoding and the exact signal we requested instead.
		if err == nil && (pid != command.Process.Pid || uint32(state)&0xff != 0x7f || syscall.Signal(uint32(state)>>8) != syscall.SIGSTOP) {
			err = fmt.Errorf("child did not stop for interruption: pid=%d status=%v", pid, state)
		}
		stopped <- err
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("child did not stop at archive barrier")
	}
	partials, err := filepath.Glob(filepath.Join(options.OutputDir, ".verified-archive-*.tar.gz"))
	if err != nil || len(partials) != 1 {
		t.Fatalf("actual partial archive missing: %v %v", partials, err)
	}
	info, err := os.Stat(partials[0])
	if err != nil || info.Size() <= 0 || info.Size() >= payloadSize {
		t.Fatalf("not interrupted during partial archive write: %v %v", info, err)
	}
	stages, err := filepath.Glob(filepath.Join(options.OutputDir, ".verified-stage-*"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("actual staged snapshot missing: %v %v", stages, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	waited = true
	if waitErr == nil {
		t.Fatal("interrupted child unexpectedly succeeded")
	}
	if ctx.Err() != nil {
		t.Fatalf("test deadline killed helper instead of barrier: %v", ctx.Err())
	}
	after, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(after, receiptBefore) {
		t.Fatalf("interrupted backup published receipt: %v", err)
	}
	unchanged, err := os.ReadFile(first.Archive)
	if err != nil || !bytes.Equal(unchanged, archiveBefore) || sha256.Sum256(unchanged) != hashBefore {
		t.Fatalf("prior immutable archive changed: %v", err)
	}
	published, err := ReadLastSuccessfulRestore(options.OutputDir)
	if err != nil || !sameJSON(published, first) {
		t.Fatalf("incomplete archive selected as successful: %v %v", published, err)
	}
	for _, root := range []string{options.DataDir, options.OutputDir} {
		lease, err := AcquireOfflineLease(root)
		if err != nil {
			t.Fatalf("exited backup retained OS lease: %s %v", root, err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Leftover temporary objects are deliberately retained: the assertion is
	// publication safety and repeatability, not automatic stale-temp cleanup.
	next, err := CreateVerifiedBackup(context.Background(), options)
	if err != nil {
		t.Fatalf("next backup after process interruption: %v; child=%s", err, diagnostics.String())
	}
	if next.Archive == first.Archive {
		t.Fatal("next backup did not include new source payload")
	}
	selected, err := ReadLastSuccessfulRestore(options.OutputDir)
	if err != nil || !sameJSON(selected, next) {
		t.Fatalf("next successful receipt missing: %v", err)
	}
	for _, root := range []string{options.DataDir, options.WorkspacePath, options.UploadsPath} {
		if err := os.Rename(root, root+"-unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := VerifyArchive(context.Background(), next.Archive, options); err != nil {
		t.Fatal(err)
	}
}
