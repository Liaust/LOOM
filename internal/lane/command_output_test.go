package lane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBoundedStreamCaptureRetainsHeadTailAndExactCount(t *testing.T) {
	capture := newBoundedStreamCapture(18)
	for _, chunk := range []string{"HEAD-", "middle-middle-", "TAIL"} {
		written, err := capture.Write([]byte(chunk))
		if err != nil || written != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, written, err)
		}
	}
	retained, truncated, total := capture.Result()
	wantTotal := int64(len("HEAD-middle-middle-TAIL"))
	if total != wantTotal || !truncated {
		t.Fatalf("capture total=%d truncated=%v, want %d true", total, truncated, wantTotal)
	}
	if !strings.HasPrefix(retained, "HEAD-") || !strings.HasSuffix(retained, "TAIL") {
		t.Fatalf("capture did not retain head and tail: %q", retained)
	}
	if !strings.Contains(retained, fmt.Sprintf("%d bytes total", wantTotal)) {
		t.Fatalf("capture omitted truncation evidence: %q", retained)
	}
}

func TestBoundedStreamCapturePreservesOrderingAcrossChunkBoundaries(t *testing.T) {
	const limit = 31
	value := strings.Repeat("0123456789", 9) + "latest-tail"
	for _, chunkSize := range []int{1, 2, 7, 19, len(value)} {
		t.Run(strconv.Itoa(chunkSize), func(t *testing.T) {
			capture := newBoundedStreamCapture(limit)
			for offset := 0; offset < len(value); offset += chunkSize {
				end := offset + chunkSize
				if end > len(value) {
					end = len(value)
				}
				_, _ = capture.Write([]byte(value[offset:end]))
			}
			retained, truncated, total := capture.Result()
			headBytes := limit / 3
			tailBytes := limit - headBytes
			if !truncated || total != int64(len(value)) || !strings.HasPrefix(retained, value[:headBytes]) || !strings.HasSuffix(retained, value[len(value)-tailBytes:]) {
				t.Fatalf("chunk=%d produced invalid capture: %q truncated=%v total=%d", chunkSize, retained, truncated, total)
			}
		})
	}

	small := "exact untruncated output"
	capture := newBoundedStreamCapture(len(small))
	_, _ = capture.Write([]byte(small[:5]))
	_, _ = capture.Write([]byte(small[5:]))
	retained, truncated, total := capture.Result()
	if retained != small || truncated || total != int64(len(small)) {
		t.Fatalf("untruncated capture changed bytes: %q truncated=%v total=%d", retained, truncated, total)
	}
}

func TestExecCommandRunnerBoundsBothStreamsAndRetainsLatestProgress(t *testing.T) {
	progress := "      4,096 100%    2.00MB/s    0:00:02 (xfr#1, to-chk=0/1)\n"
	stdoutPrefixBytes := 4*DefaultCommandOutputLimit + 37
	stderrBytes := 3*DefaultCommandOutputLimit + 19
	output, err := execCommandRunner(context.Background(), os.Args[0],
		"-test.run=TestLaneExecCommandRunnerHelper", "--", "output",
		strconv.Itoa(stdoutPrefixBytes), strconv.Itoa(stderrBytes), progress)
	if err != nil {
		t.Fatalf("execCommandRunner: %v", err)
	}
	if output.StdoutBytes != int64(stdoutPrefixBytes+len(progress)) || output.StderrBytes != int64(stderrBytes) {
		t.Fatalf("wrong byte totals: %#v", output)
	}
	if !output.StdoutTruncated || !output.StderrTruncated {
		t.Fatalf("expected both streams truncated: %#v", output)
	}
	if !strings.HasPrefix(output.Stdout, "stdout-head-") || !strings.Contains(output.Stderr, "stderr-head-") {
		t.Fatalf("bounded output lost diagnostic heads: %#v", output)
	}
	if !strings.HasSuffix(output.Stdout, progress) {
		t.Fatalf("bounded stdout lost latest progress tail: %q", output.Stdout)
	}
	parsed := parseRsyncProgress(output.Stdout+"\n"+output.Stderr, int64(stdoutPrefixBytes), time.Now().UTC())
	if !parsed.Observed || parsed.Percent != 100 || parsed.ETASeconds != 2 {
		t.Fatalf("retained tail progress not parsed: %#v", parsed)
	}
}

func TestExecCommandRunnerPreservesBoundedOutputOnExitError(t *testing.T) {
	stdoutBytes := 2*DefaultCommandOutputLimit + 7
	stderrBytes := 2*DefaultCommandOutputLimit + 11
	output, err := execCommandRunner(context.Background(), os.Args[0],
		"-test.run=TestLaneExecCommandRunnerHelper", "--", "exit-error",
		strconv.Itoa(stdoutBytes), strconv.Itoa(stderrBytes))
	if err == nil {
		t.Fatal("expected child exit error")
	}
	if output.StdoutBytes != int64(stdoutBytes) || output.StderrBytes != int64(stderrBytes) || !output.StdoutTruncated || !output.StderrTruncated {
		t.Fatalf("exit error lost bounded output evidence: %#v, %v", output, err)
	}
}

func TestExecCommandRunnerPreservesOutputOnCancellation(t *testing.T) {
	readyPath := t.TempDir() + "/ready"
	const stdoutBytes = 4096
	const stderrBytes = 3072
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(readyPath); err == nil {
				cancel()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	output, err := execCommandRunner(ctx, os.Args[0],
		"-test.run=TestLaneExecCommandRunnerHelper", "--", "wait",
		strconv.Itoa(stdoutBytes), strconv.Itoa(stderrBytes), readyPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("exec cancellation error = %v", err)
	}
	if output.StdoutBytes != stdoutBytes || output.StderrBytes != stderrBytes {
		t.Fatalf("cancellation lost exact drained output: %#v", output)
	}
	if output.StdoutTruncated || output.StderrTruncated {
		t.Fatalf("small cancellation output unexpectedly truncated: %#v", output)
	}
}

func TestLaneExecCommandRunnerHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+3 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	stdoutBytes, stdoutErr := strconv.Atoi(os.Args[separator+2])
	stderrBytes, stderrErr := strconv.Atoi(os.Args[separator+3])
	if stdoutErr != nil || stderrErr != nil || stdoutBytes < 0 || stderrBytes < 0 {
		os.Exit(91)
	}
	writePattern(os.Stdout, stdoutBytes, "stdout-head-")
	writePattern(os.Stderr, stderrBytes, "stderr-head-")
	switch mode {
	case "output":
		if separator+4 < len(os.Args) {
			_, _ = os.Stdout.WriteString(os.Args[separator+4])
		}
		os.Exit(0)
	case "exit-error":
		os.Exit(7)
	case "wait":
		if separator+4 >= len(os.Args) || os.WriteFile(os.Args[separator+4], []byte("ready"), 0o600) != nil {
			os.Exit(92)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		os.Exit(93)
	}
}

func writePattern(file *os.File, total int, prefix string) {
	value := prefix + strings.Repeat("x", total)
	_, _ = file.WriteString(value[:total])
}
