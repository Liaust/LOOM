package loomcli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

const storageBenchmarkSchema = "storage_benchmark.result.v0.6.7"

type storageBenchmarkFlags struct {
	SizeMB      int
	LocalPath   string
	SMBPath     string
	RsyncTarget string
	SmallFiles  int
	Keep        bool
	SkipLocal   bool
	SkipSMB     bool
	SkipSmall   bool
	Timeout     time.Duration
}

type storageBenchmarkReport struct {
	SchemaVersion string                   `json:"schema_version"`
	GeneratedAt   time.Time                `json:"generated_at"`
	SizeBytes     int64                    `json:"size_bytes"`
	Results       []storageBenchmarkResult `json:"results"`
	Notes         []string                 `json:"notes,omitempty"`
}

type storageBenchmarkResult struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Path       string   `json:"path,omitempty"`
	Target     string   `json:"target,omitempty"`
	Bytes      int64    `json:"bytes"`
	DurationMS int64    `json:"duration_ms"`
	MiBPerSec  float64  `json:"mib_per_sec"`
	Command    []string `json:"command,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func newStorageBenchmarkCommand(opts *options) *cobra.Command {
	flags := storageBenchmarkFlags{
		SizeMB:     16,
		LocalPath:  defaultStorageBenchmarkPath(filepath.Join("loom-box", "Documents", ".loom-acceptance", "debug", "storage-benchmark", "local")),
		SMBPath:    defaultStorageBenchmarkPath(filepath.Join("loom-storage", "main", "Documents", ".loom-acceptance", "debug", "storage-benchmark", "smb")),
		SmallFiles: 100,
		Timeout:    15 * time.Minute,
	}
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Write bounded acceptance files to compare local, SMB, and optional rsync storage speed",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := runStorageBenchmark(cmd.Context(), flags)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				for _, result := range report.Results {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s %.2fMiB/s\n", result.Name, result.Status, result.MiBPerSec)
				}
				return nil
			}
			renderStorageBenchmarkReport(cmd, report)
			return nil
		},
	}
	cmd.Flags().IntVar(&flags.SizeMB, "size-mb", flags.SizeMB, "benchmark file size in MiB")
	cmd.Flags().StringVar(&flags.LocalPath, "local-path", flags.LocalPath, "local acceptance directory for raw disk write")
	cmd.Flags().StringVar(&flags.SMBPath, "smb-path", flags.SMBPath, "SMB-mounted acceptance directory for raw Finder/SMB write")
	cmd.Flags().StringVar(&flags.RsyncTarget, "rsync-target", "", "optional rsync target directory, for example loom-main:/srv/loom/storage/main/Documents/.loom-acceptance/debug/storage-benchmark/rsync")
	cmd.Flags().IntVar(&flags.SmallFiles, "small-files", flags.SmallFiles, "number of small files for SMB/listing behavior benchmark")
	cmd.Flags().BoolVar(&flags.Keep, "keep", false, "keep generated benchmark files instead of deleting local and SMB files")
	cmd.Flags().BoolVar(&flags.SkipLocal, "skip-local", false, "skip raw local disk write")
	cmd.Flags().BoolVar(&flags.SkipSMB, "skip-smb", false, "skip raw SMB-mounted write")
	cmd.Flags().BoolVar(&flags.SkipSmall, "skip-small", false, "skip many-small-file write/list benchmark")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", flags.Timeout, "per-command timeout for optional rsync benchmark")
	return cmd
}

func runStorageBenchmark(ctx context.Context, flags storageBenchmarkFlags) storageBenchmarkReport {
	sizeBytes := int64(flags.SizeMB) * 1024 * 1024
	if sizeBytes <= 0 {
		sizeBytes = 16 * 1024 * 1024
	}
	report := storageBenchmarkReport{
		SchemaVersion: storageBenchmarkSchema,
		GeneratedAt:   time.Now().UTC(),
		SizeBytes:     sizeBytes,
		Notes: []string{
			"Use loom storage main-documents status --json for importer latency.",
			"Use loom storage filesystem status --json for bounded physical-root and catalog health.",
			"All default paths are under .loom-acceptance/debug; do not use production roots as benchmark scratch space.",
		},
	}
	if !flags.SkipLocal {
		report.Results = append(report.Results, runWriteReadBenchmark("local", flags.LocalPath, sizeBytes, flags.Keep)...)
		if !flags.SkipSmall {
			report.Results = append(report.Results, runSmallFilesBenchmark("local_small_files", flags.LocalPath, flags.SmallFiles, flags.Keep))
		}
	}
	if !flags.SkipSMB {
		report.Results = append(report.Results, runWriteReadBenchmark("smb", flags.SMBPath, sizeBytes, flags.Keep)...)
		if !flags.SkipSmall {
			report.Results = append(report.Results, runSmallFilesBenchmark("smb_small_files", flags.SMBPath, flags.SmallFiles, flags.Keep))
		}
	}
	if strings.TrimSpace(flags.RsyncTarget) != "" {
		report.Results = append(report.Results, runRsyncBenchmark(ctx, flags, sizeBytes))
	}
	return report
}

func runWriteReadBenchmark(name, dir string, sizeBytes int64, keep bool) []storageBenchmarkResult {
	write := runWriteBenchmark(name+"_write", dir, sizeBytes, keep)
	results := []storageBenchmarkResult{write}
	if write.Status != "succeeded" || write.Path == "" {
		return results
	}
	read := runReadBenchmark(name+"_read", write.Path, sizeBytes)
	results = append(results, read)
	if !keep {
		_ = os.Remove(write.Path)
	}
	return results
}

func runWriteBenchmark(name, dir string, sizeBytes int64, keep bool) storageBenchmarkResult {
	resolvedDir := expandStorageBenchmarkPath(dir)
	result := storageBenchmarkResult{Name: name, Path: resolvedDir, Bytes: sizeBytes}
	if err := validateStorageBenchmarkScratchPath(resolvedDir); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if err := os.MkdirAll(resolvedDir, 0o755); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	target := filepath.Join(resolvedDir, storageBenchmarkFileName())
	start := time.Now()
	if err := writeStorageBenchmarkFile(target, sizeBytes); err != nil {
		result.Status = "failed"
		result.Path = target
		result.Error = err.Error()
		result.DurationMS = durationMSSinceCLI(start)
		result.MiBPerSec = storageBenchmarkMiBPerSec(sizeBytes, result.DurationMS)
		return result
	}
	result.Status = "succeeded"
	result.Path = target
	result.DurationMS = durationMSSinceCLI(start)
	result.MiBPerSec = storageBenchmarkMiBPerSec(sizeBytes, result.DurationMS)
	return result
}

func runReadBenchmark(name, path string, sizeBytes int64) storageBenchmarkResult {
	result := storageBenchmarkResult{Name: name, Path: path, Bytes: sizeBytes}
	if err := validateStorageBenchmarkScratchPath(filepath.Dir(path)); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	start := time.Now()
	file, err := os.Open(path)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	defer file.Close()
	n, err := io.Copy(io.Discard, file)
	result.Bytes = n
	result.DurationMS = durationMSSinceCLI(start)
	result.MiBPerSec = storageBenchmarkMiBPerSec(n, result.DurationMS)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	result.Status = "succeeded"
	return result
}

func runSmallFilesBenchmark(name, dir string, smallFiles int, keep bool) storageBenchmarkResult {
	resolvedDir := expandStorageBenchmarkPath(dir)
	result := storageBenchmarkResult{Name: name, Path: resolvedDir}
	if err := validateStorageBenchmarkScratchPath(resolvedDir); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if smallFiles <= 0 {
		smallFiles = 100
	}
	targetDir := filepath.Join(resolvedDir, fmt.Sprintf("small-files-%d", time.Now().UTC().UnixNano()))
	start := time.Now()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	content := []byte("loom storage benchmark\n")
	for i := 0; i < smallFiles; i++ {
		path := filepath.Join(targetDir, fmt.Sprintf("file-%05d.txt", i))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			if !keep {
				_ = os.RemoveAll(targetDir)
			}
			return result
		}
		result.Bytes += int64(len(content))
	}
	entries, err := os.ReadDir(targetDir)
	result.DurationMS = durationMSSinceCLI(start)
	result.MiBPerSec = storageBenchmarkMiBPerSec(result.Bytes, result.DurationMS)
	result.Path = targetDir
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	} else if len(entries) != smallFiles {
		result.Status = "failed"
		result.Error = fmt.Sprintf("listed %d files, expected %d", len(entries), smallFiles)
	} else {
		result.Status = "succeeded"
	}
	if !keep {
		_ = os.RemoveAll(targetDir)
	}
	return result
}

func runRsyncBenchmark(ctx context.Context, flags storageBenchmarkFlags, sizeBytes int64) storageBenchmarkResult {
	sourceDir := expandStorageBenchmarkPath(filepath.Join("~", "loom-box", "Documents", ".loom-acceptance", "debug", "storage-benchmark", "rsync-source"))
	result := storageBenchmarkResult{Name: "rsync", Target: flags.RsyncTarget, Bytes: sizeBytes}
	if err := validateStorageBenchmarkScratchPath(sourceDir); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if err := validateStorageBenchmarkTarget(flags.RsyncTarget); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	source := filepath.Join(sourceDir, storageBenchmarkFileName())
	if err := writeStorageBenchmarkFile(source, sizeBytes); err != nil {
		result.Status = "failed"
		result.Path = source
		result.Error = err.Error()
		return result
	}
	if !flags.Keep {
		defer os.Remove(source)
	}
	timeout := flags.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := strings.TrimRight(flags.RsyncTarget, "/") + "/"
	args := []string{"-a", "--", source, target}
	result.Command = append([]string{"rsync"}, args...)
	start := time.Now()
	output, err := exec.CommandContext(commandCtx, "rsync", args...).CombinedOutput()
	result.DurationMS = durationMSSinceCLI(start)
	result.MiBPerSec = storageBenchmarkMiBPerSec(sizeBytes, result.DurationMS)
	if err != nil {
		result.Status = "failed"
		result.Error = strings.TrimSpace(string(output))
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result
	}
	result.Status = "succeeded"
	return result
}

func validateStorageBenchmarkScratchPath(path string) error {
	if !storageBenchmarkPathHasAcceptanceDebug(path) {
		return fmt.Errorf("benchmark path must stay under .loom-acceptance/debug: %s", path)
	}
	return nil
}

func validateStorageBenchmarkTarget(target string) error {
	if !storageBenchmarkPathHasAcceptanceDebug(target) {
		return fmt.Errorf("benchmark target must stay under .loom-acceptance/debug: %s", target)
	}
	return nil
}

func storageBenchmarkPathHasAcceptanceDebug(value string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	return strings.Contains(value, "/.loom-acceptance/debug/") ||
		strings.HasSuffix(value, "/.loom-acceptance/debug")
}

func renderStorageBenchmarkReport(cmd *cobra.Command, report storageBenchmarkReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage benchmark\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Size: %s\n", storageBenchmarkFormatBytes(report.SizeBytes))
	if len(report.Results) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TARGET\tSTATUS\tDURATION\tTHROUGHPUT\tPATH")
		for _, result := range report.Results {
			path := firstNonEmptyString(result.Path, result.Target)
			fmt.Fprintf(writer, "%s\t%s\t%s\t%.2f MiB/s\t%s\n",
				result.Name,
				result.Status,
				formatDurationMS(result.DurationMS),
				result.MiBPerSec,
				path,
			)
			if result.Error != "" {
				fmt.Fprintf(writer, "\t\t\t\terror=%s\n", result.Error)
			}
		}
		_ = writer.Flush()
	}
	if len(report.Notes) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Notes:")
		for _, note := range report.Notes {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", note)
		}
	}
}

func writeStorageBenchmarkFile(path string, sizeBytes int64) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	chunk := make([]byte, 4*1024*1024)
	if _, err := io.ReadFull(rand.Reader, chunk); err != nil {
		return err
	}
	remaining := sizeBytes
	for remaining > 0 {
		writeSize := int64(len(chunk))
		if remaining < writeSize {
			writeSize = remaining
		}
		if _, err := file.Write(chunk[:writeSize]); err != nil {
			return err
		}
		remaining -= writeSize
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func storageBenchmarkFileName() string {
	return fmt.Sprintf("loom-storage-benchmark-%d.bin", time.Now().UTC().UnixNano())
}

func defaultStorageBenchmarkPath(rel string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join("~", rel)
	}
	return filepath.Join(home, rel)
}

func expandStorageBenchmarkPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func storageBenchmarkMiBPerSec(bytes int64, durationMS int64) float64 {
	if bytes <= 0 || durationMS <= 0 {
		return 0
	}
	seconds := float64(durationMS) / 1000
	if seconds <= 0 {
		return 0
	}
	value := (float64(bytes) / 1024 / 1024) / seconds
	return math.Round(value*100) / 100
}

func storageBenchmarkFormatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(value)
	for _, unit := range units {
		f = f / 1024
		if f < 1024 {
			return fmt.Sprintf("%.2f %s", f, unit)
		}
	}
	return fmt.Sprintf("%.2f PiB", f/1024)
}

func durationMSSinceCLI(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	return time.Since(start).Milliseconds()
}
