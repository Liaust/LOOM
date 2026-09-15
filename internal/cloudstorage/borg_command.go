package cloudstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	borgFailureDiagnosticLimit = 4096
	borgFailureInspectLimit    = 64 << 10
	borgFailureOmissionMarker  = "\n[borg diagnostic output omitted]\n"
	borgFailureRedactedMarker  = "[credential material redacted]"
)

type BorgCommand struct {
	Args []string
	Dir  string
}

type BorgExecFunc func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error)
type BorgStreamExecFunc func(ctx context.Context, binary string, command BorgCommand, env []string, stdout io.Writer) error

// BorgExecutionError preserves the causal execution error while exposing only
// bounded, normalized command output suitable for durable operational evidence.
// It never includes the command environment.
type BorgExecutionError struct {
	Command             string
	Operation           string
	Diagnostic          string
	DiagnosticTruncated bool
	Err                 error
}

func (e *BorgExecutionError) Error() string {
	cause := "unknown execution error"
	if e.Err != nil {
		if normalized, _ := boundedBorgFailureDiagnostic([]byte(e.Err.Error())); normalized != "" {
			cause = normalized
		}
	}
	message := fmt.Sprintf("borg %s failed: %s", e.Command, cause)
	if e.Diagnostic != "" {
		message += "; bounded diagnostic: " + e.Diagnostic
	}
	return message
}

func (e *BorgExecutionError) Unwrap() error {
	return e.Err
}

type BorgCommandRunner struct {
	Config            Config
	Exec              BorgExecFunc
	StreamExec        BorgStreamExecFunc
	DisableRemoteLock bool
	RemoteLockWait    *time.Duration
	observeCommand    func(BorgCommand)
	authority         borgCommandAuthority
}

type borgCommandAuthority string

const (
	borgAuthorityWriter    borgCommandAuthority = "writer"
	borgAuthorityRetention borgCommandAuthority = "retention"
)

func (r BorgCommandRunner) RunStream(ctx context.Context, command BorgCommand, stdout io.Writer) error {
	if stdout == nil {
		return fmt.Errorf("Borg streaming output writer is required")
	}
	if err := authorizeBorgCommand(r.authority, command); err != nil {
		return err
	}
	cfg, err := NormalizeConfig(r.Config)
	if err != nil {
		return err
	}
	if err := validateBorgCommandConfig(cfg); err != nil {
		return err
	}
	env := borgEnv(cfg)
	operationName := command.Name()
	command = command.withCommonArgs(cfg)
	run := func(ctx context.Context) error {
		if r.observeCommand != nil {
			r.observeCommand(command)
		}
		if r.StreamExec != nil {
			return r.StreamExec(ctx, cfg.Snapshots.Borg.Binary, command, env, stdout)
		}
		if r.Exec != nil {
			out, err := r.Exec(ctx, cfg.Snapshots.Borg.Binary, command, env)
			if len(out) > 0 {
				if _, writeErr := stdout.Write(out); writeErr != nil && err == nil {
					err = writeErr
				}
			}
			return err
		}
		return defaultBorgStreamExec(ctx, cfg.Snapshots.Borg.Binary, command, env, stdout)
	}
	var runErr error
	if r.DisableRemoteLock {
		runErr = run(ctx)
	} else {
		lockWait := RemoteLockEffectfulWait(cfg)
		if r.RemoteLockWait != nil {
			lockWait = *r.RemoteLockWait
		}
		runErr = WithRemoteLock(ctx, cfg, RemoteLockOptions{Operation: "borg." + operationName, Wait: lockWait}, run)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			runErr = errors.Join(runErr, ctx.Err())
		}
		var nested *BorgExecutionError
		diagnostic, truncated := boundedBorgFailureDiagnostic([]byte(runErr.Error()))
		if errors.As(runErr, &nested) {
			diagnostic, truncated = nested.Diagnostic, nested.DiagnosticTruncated
		}
		return &BorgExecutionError{Command: strings.Join(command.Args, " "), Operation: operationName, Diagnostic: diagnostic, DiagnosticTruncated: truncated, Err: runErr}
	}
	return nil
}

func NewBorgCommandRunner(cfg Config) BorgCommandRunner {
	return BorgCommandRunner{Config: cfg}
}

func (r BorgCommandRunner) Run(ctx context.Context, command BorgCommand) ([]byte, error) {
	if err := authorizeBorgCommand(r.authority, command); err != nil {
		return nil, err
	}
	cfg, err := NormalizeConfig(r.Config)
	if err != nil {
		return nil, err
	}
	if err := validateBorgCommandConfig(cfg); err != nil {
		return nil, err
	}
	env := borgEnv(cfg)
	execFunc := r.Exec
	if execFunc == nil {
		execFunc = defaultBorgExec
	}
	operationName := command.Name()
	command = command.withCommonArgs(cfg)
	var out []byte
	run := func(ctx context.Context) error {
		if r.observeCommand != nil {
			r.observeCommand(command)
		}
		var err error
		out, err = execFunc(ctx, cfg.Snapshots.Borg.Binary, command, env)
		return err
	}
	var runErr error
	if r.DisableRemoteLock {
		runErr = run(ctx)
	} else {
		lockWait := RemoteLockEffectfulWait(cfg)
		if r.RemoteLockWait != nil {
			lockWait = *r.RemoteLockWait
		}
		runErr = WithRemoteLock(ctx, cfg, RemoteLockOptions{Operation: "borg." + operationName, Wait: lockWait}, run)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			runErr = errors.Join(runErr, ctx.Err())
		}
		diagnostic, truncated := boundedBorgFailureDiagnostic(out)
		return out, &BorgExecutionError{
			Command:             strings.Join(command.Args, " "),
			Operation:           operationName,
			Diagnostic:          diagnostic,
			DiagnosticTruncated: truncated,
			Err:                 runErr,
		}
	}
	return out, nil
}

func boundedBorgFailureDiagnostic(raw []byte) (string, bool) {
	normalized, truncated := boundedNormalizedBorgFailureOutput(raw, borgFailureInspectLimit)
	lines := strings.Split(normalized, "\n")
	sanitized := make([]string, 0, len(lines))
	inPrivateKey := false
	for _, line := range lines {
		line = redactBorgDiagnosticURIUserinfo(line)
		lower := strings.ToLower(strings.TrimSpace(line))
		if inPrivateKey {
			if strings.Contains(lower, "-----end ") && strings.Contains(lower, "private key-----") {
				inPrivateKey = false
			}
			continue
		}
		if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
			sanitized = append(sanitized, borgFailureRedactedMarker)
			inPrivateKey = true
			continue
		}
		if borgDiagnosticLineContainsCredential(line, lower) {
			sanitized = append(sanitized, borgFailureRedactedMarker)
			continue
		}
		sanitized = append(sanitized, line)
	}
	diagnostic := strings.TrimSpace(strings.Join(sanitized, "\n"))
	if len(diagnostic) > borgFailureDiagnosticLimit {
		truncated = true
	}
	if truncated {
		diagnostic = boundBorgDiagnosticHeadTail(diagnostic, borgFailureDiagnosticLimit)
	}
	return diagnostic, truncated
}

func boundedNormalizedBorgFailureOutput(raw []byte, limit int) (string, bool) {
	if len(raw) <= limit {
		return normalizeBorgFailureControls(raw), false
	}
	budget := limit - len(borgFailureOmissionMarker)
	headBytes := budget / 2
	tailBytes := budget - headBytes
	head := normalizeBorgFailureControls(completeBorgFailureHead(raw, headBytes))
	tail := normalizeBorgFailureControls(completeBorgFailureTail(raw, tailBytes))
	return strings.TrimRight(head, " \t\n") + borgFailureOmissionMarker + strings.TrimLeft(tail, " \t\n"), true
}

// completeBorgFailureHead and completeBorgFailureTail discard a line cut by
// raw byte bounding. Redaction is line-aware, so retaining an unidentified
// fragment could detach credential material from the prefix that identifies it.
func completeBorgFailureHead(raw []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(raw) <= limit {
		return raw
	}
	candidate := raw[:limit]
	if boundary := bytes.LastIndexAny(candidate, "\r\n"); boundary >= 0 {
		return candidate[:boundary+1]
	}
	return nil
}

func completeBorgFailureTail(raw []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(raw) <= limit {
		return raw
	}
	start := len(raw) - limit
	if raw[start-1] == '\n' || raw[start-1] == '\r' {
		return raw[start:]
	}
	candidate := raw[start:]
	if boundary := bytes.IndexAny(candidate, "\r\n"); boundary >= 0 {
		return candidate[boundary+1:]
	}
	return nil
}

func boundBorgDiagnosticHeadTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	budget := limit - len(borgFailureOmissionMarker)
	headBytes := budget / 2
	tailBytes := budget - headBytes
	head := truncateValidUTF8(value, headBytes)
	tailStart := len(value) - tailBytes
	for tailStart < len(value) && !utf8.ValidString(value[tailStart:]) {
		tailStart++
	}
	tail := value[tailStart:]
	return strings.TrimRight(head, " \t\n") + borgFailureOmissionMarker + strings.TrimLeft(tail, " \t\n")
}

func normalizeBorgFailureControls(raw []byte) string {
	withoutANSI := make([]byte, 0, len(raw))
	for index := 0; index < len(raw); {
		if raw[index] != 0x1b {
			withoutANSI = append(withoutANSI, raw[index])
			index++
			continue
		}
		index++
		if index >= len(raw) {
			break
		}
		switch raw[index] {
		case '[':
			index++
			for index < len(raw) {
				final := raw[index] >= 0x40 && raw[index] <= 0x7e
				index++
				if final {
					break
				}
			}
		case ']':
			index++
			for index < len(raw) {
				if raw[index] == 0x07 {
					index++
					break
				}
				if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
					index += 2
					break
				}
				index++
			}
		default:
			index++
		}
	}
	valid := strings.ToValidUTF8(string(withoutANSI), "�")
	var normalized strings.Builder
	normalized.Grow(len(valid))
	for _, value := range valid {
		switch value {
		case '\n':
			normalized.WriteRune(value)
		case '\r':
			normalized.WriteRune('\n')
		case '\t':
			normalized.WriteByte(' ')
		default:
			if unicode.IsControl(value) {
				normalized.WriteByte(' ')
				continue
			}
			normalized.WriteRune(value)
		}
	}
	return normalized.String()
}

func borgDiagnosticLineContainsCredential(line, lower string) bool {
	for _, marker := range []string{
		"borg_passphrase=", "borg_passcommand=", "passphrase=", "passphrase:",
		"password=", "password:", "authorization:", "private_key=", "secret=", "token=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if equals := strings.IndexByte(line, '='); equals > 0 {
		name := strings.TrimSpace(line[:equals])
		if name != "" {
			environmentName := true
			for _, value := range name {
				if (value < 'A' || value > 'Z') && (value < '0' || value > '9') && value != '_' {
					environmentName = false
					break
				}
			}
			if environmentName {
				return true
			}
		}
	}
	return false
}

func redactBorgDiagnosticURIUserinfo(line string) string {
	var redacted strings.Builder
	cursor := 0
	for cursor < len(line) {
		relativeScheme := strings.Index(line[cursor:], "://")
		if relativeScheme < 0 {
			break
		}
		authorityStart := cursor + relativeScheme + 3
		rest := line[authorityStart:]
		at := strings.IndexByte(rest, '@')
		boundary := strings.IndexAny(rest, "/ \t")
		redacted.WriteString(line[cursor:authorityStart])
		if at >= 0 && (boundary < 0 || at < boundary) {
			redacted.WriteString("[redacted]@")
			cursor = authorityStart + at + 1
			continue
		}
		cursor = authorityStart
	}
	if cursor == 0 {
		return line
	}
	redacted.WriteString(line[cursor:])
	return redacted.String()
}

func truncateValidUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func authorizeBorgCommand(authority borgCommandAuthority, command BorgCommand) error {
	if authority == "" {
		authority = borgAuthorityWriter
	}
	name, err := requestedBorgCommandName(command)
	if err != nil {
		return err
	}
	writerCommands := map[string]bool{
		"check": true, "create": true, "export-tar": true, "extract": true,
		"info": true, "init": true, "list": true, "rename": true,
	}
	retentionCommands := map[string]bool{
		"check": true, "compact": true, "delete": true, "export-tar": true, "extract": true,
		"info": true, "list": true, "prune": true,
	}
	if authority == borgAuthorityWriter {
		if writerCommands[name] {
			return nil
		}
		if retentionCommands[name] {
			return fmt.Errorf("Borg %s requires the separately configured retention authority", name)
		}
		return fmt.Errorf("Borg command %q is not permitted for the routine writer", name)
	}
	if authority != borgAuthorityRetention {
		return fmt.Errorf("unknown Borg command authority %q", authority)
	}
	if !retentionCommands[name] {
		return fmt.Errorf("Borg command %q is not permitted for the retention authority", name)
	}
	return nil
}

func requestedBorgCommandName(command BorgCommand) (string, error) {
	if len(command.Args) == 0 {
		return "", fmt.Errorf("Borg command is required")
	}
	name := strings.TrimSpace(command.Args[0])
	if name == "" || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("Borg command must be the first argument; global option prefixes are not permitted")
	}
	return name, nil
}

func (c BorgCommand) Name() string {
	if len(c.Args) >= 3 && c.Args[0] == "--lock-wait" {
		if _, err := strconv.Atoi(c.Args[1]); err == nil {
			return c.Args[2]
		}
	}
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return "command"
}

func (c BorgCommand) withCommonArgs(cfg Config) BorgCommand {
	lockWait := cfg.Snapshots.Borg.LockWaitSeconds
	if lockWait <= 0 {
		return c
	}
	out := c
	out.Args = append([]string{"--lock-wait", strconv.Itoa(lockWait)}, c.Args...)
	return out
}

func borgEnv(cfg Config) []string {
	borg := cfg.Snapshots.Borg
	env := []string{
		"BORG_REPO=" + borg.Repository,
		"BORG_CACHE_DIR=" + borg.CacheDir,
		"BORG_SECURITY_DIR=" + borg.SecurityDir,
	}
	if borg.RSH != "" {
		env = append(env, "BORG_RSH="+strictBorgRSH(borg.RSH))
	} else {
		env = append(env, "BORG_RSH="+strictBorgRSH("ssh"))
	}
	if borg.PassphraseFile != "" {
		cat := resolveCommandPath("cat")
		env = append(env, "BORG_PASSCOMMAND="+cat+" "+shellQuote(borg.PassphraseFile))
	}
	return env
}

func strictBorgRSH(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "ssh"
	}
	for _, option := range []string{
		"BatchMode=yes",
		"IdentitiesOnly=yes",
		"ConnectTimeout=8",
		"ConnectionAttempts=1",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=2",
	} {
		key := strings.SplitN(option, "=", 2)[0]
		if strings.Contains(value, key) {
			continue
		}
		value += " -o " + option
	}
	return value
}

func defaultBorgExec(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, resolveCommandPath(binary), command.Args...)
	if strings.TrimSpace(command.Dir) != "" {
		cmd.Dir = command.Dir
	}
	cmd.Env = append(os.Environ(), env...)
	if command.Name() == "extract" && strings.TrimSpace(command.Dir) != "" {
		var stdout, stderr boundedBorgExtractOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err != nil {
			return append(stdout.Bytes(), stderr.Bytes()...), err
		}
		return stdout.Bytes(), nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}

func defaultBorgStreamExec(ctx context.Context, binary string, command BorgCommand, env []string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, resolveCommandPath(binary), command.Args...)
	if strings.TrimSpace(command.Dir) != "" {
		cmd.Dir = command.Dir
	}
	cmd.Env = append(os.Environ(), env...)
	var stderr boundedBorgExtractOutput
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		diagnostic, truncated := boundedBorgFailureDiagnostic(stderr.Bytes())
		return &BorgExecutionError{Operation: command.Name(), Diagnostic: diagnostic, DiagnosticTruncated: truncated, Err: err}
	}
	return nil
}

func resolveCommandPath(binary string) string {
	binary = strings.TrimSpace(binary)
	if binary == "" || strings.ContainsRune(binary, os.PathSeparator) {
		return binary
	}
	if resolved, err := exec.LookPath(binary); err == nil {
		return resolved
	}
	for _, dir := range []string{"/run/current-system/sw/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		candidate := dir + string(os.PathSeparator) + binary
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return binary
}

func validateBorgCommandConfig(cfg Config) error {
	borg := cfg.Snapshots.Borg
	if strings.TrimSpace(borg.Binary) == "" {
		return fmt.Errorf("borg binary is required")
	}
	if strings.TrimSpace(borg.Repository) == "" {
		return fmt.Errorf("borg repository is required; configure snapshots.borg.repository")
	}
	if strings.TrimSpace(borg.PassphraseFile) == "" {
		return fmt.Errorf("borg passphrase file is required; configure snapshots.borg.passphrase_file")
	}
	linkInfo, err := os.Lstat(borg.PassphraseFile)
	if err != nil {
		return fmt.Errorf("borg passphrase file is not readable: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("borg passphrase file must not be a symlink: %s", borg.PassphraseFile)
	}
	info, err := os.Stat(borg.PassphraseFile)
	if err != nil {
		return fmt.Errorf("borg passphrase file is not readable: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("borg passphrase file points to a directory: %s", borg.PassphraseFile)
	}
	for _, dir := range []string{borg.CacheDir, borg.SecurityDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("prepare borg state directory %s: %w", dir, err)
		}
	}
	return nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Payload extraction has no data-bearing stdout. Bound its diagnostic streams
// at capture, retaining a prefix and explicit loss marker rather than growing
// process memory with an arbitrarily large stderr stream.
type boundedBorgExtractOutput struct {
	data      []byte
	truncated bool
}

func (b *boundedBorgExtractOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := borgFailureInspectLimit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedBorgExtractOutput) Bytes() []byte {
	if b.truncated {
		return append(append([]byte(nil), b.data...), borgFailureOmissionMarker...)
	}
	return append([]byte(nil), b.data...)
}
