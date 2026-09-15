package knowledge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

type HeavyCommandResult struct {
	Output    []byte
	TempBytes int64
}

func RunHeavyCommand(ctx context.Context, policy HeavyResourcePolicy, name string, args ...string) (HeavyCommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, policy.Timeout())
	defer cancel()
	tempRoot, err := os.MkdirTemp("", "loom-knowledge-heavy-*")
	if err != nil {
		return HeavyCommandResult{}, err
	}
	defer os.RemoveAll(tempRoot)
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = tempRoot
	command.Env = append(os.Environ(), "TMPDIR="+tempRoot)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := command.CombinedOutput()
	if ctx.Err() != nil && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		return HeavyCommandResult{Output: output}, ctx.Err()
	}
	bytes, sizeErr := directorySize(tempRoot)
	if sizeErr != nil {
		return HeavyCommandResult{}, sizeErr
	}
	if bytes > policy.MaxTempBytes {
		return HeavyCommandResult{}, fmt.Errorf("%w: temporary storage budget exceeded", ErrInvalid)
	}
	return HeavyCommandResult{Output: output, TempBytes: bytes}, err
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
