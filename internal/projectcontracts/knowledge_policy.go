package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

// NormalizeKnowledgeSourcePolicy validates intent without enabling host services.
func NormalizeKnowledgeSourcePolicy(k KnowledgeDeclaration) (*KnowledgeSourcePolicy, error) {
	if err := watchedroots.ValidatePatterns(k.Include); err != nil {
		return nil, fmt.Errorf("knowledge.include: %w", err)
	}
	if err := watchedroots.ValidatePatterns(k.Exclude); err != nil {
		return nil, fmt.Errorf("knowledge.exclude: %w", err)
	}
	if k.Workspace != nil {
		if err := ValidateNotesWorkspacePath(k.Workspace.Path); err != nil {
			return nil, err
		}
		if err := watchedroots.ValidatePatterns(k.Workspace.Include); err != nil {
			return nil, fmt.Errorf("knowledge.workspace.include: %w", err)
		}
		if err := watchedroots.ValidatePatterns(k.Workspace.Exclude); err != nil {
			return nil, fmt.Errorf("knowledge.workspace.exclude: %w", err)
		}
	}
	if k.Refresh == nil && k.Processing == nil && k.Workspace == nil {
		return nil, nil
	}
	out := &KnowledgeSourcePolicy{}
	if k.Workspace != nil {
		out.Workspace = &KnowledgeWorkspacePolicy{
			Path:    k.Workspace.Path,
			Include: normalizePolicyPatterns(firstNonEmptyStrings(k.Workspace.Include, defaultNotesIncludes)),
			Exclude: normalizePolicyPatterns(k.Workspace.Exclude),
		}
	}
	if k.Refresh != nil {
		quiet, err := knowledgeDuration(k.Refresh.QuietFor, 10*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("knowledge.refresh.quiet_for: %w", err)
		}
		maximum, err := knowledgeDuration(k.Refresh.MaxWait, 30*time.Minute)
		if err != nil || maximum <= 0 || maximum < quiet {
			return nil, fmt.Errorf("knowledge.refresh.max_wait must be positive, at least quiet_for and at most 24h")
		}
		out.Refresh = &KnowledgeRefreshPolicy{QuietForSeconds: int64(quiet / time.Second), MaxWaitSeconds: int64(maximum / time.Second)}
	}
	if k.Processing != nil {
		p := *k.Processing
		if err := ValidateKnowledgeProcessingPolicy(p); err != nil {
			return nil, err
		}
		out.Processing = &p
	}
	return out, nil
}

// DecodeKnowledgeSourcePolicy accepts only normalized compiler evidence.
func DecodeKnowledgeSourcePolicy(raw []byte) (KnowledgeSourcePolicy, error) {
	value, err := DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		return KnowledgeSourcePolicy{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return KnowledgeSourcePolicy{}, fmt.Errorf("knowledge policy must be an object")
	}
	for key := range fields {
		switch key {
		case "refresh", "processing", "workspace", "files":
		default:
			return KnowledgeSourcePolicy{}, fmt.Errorf("knowledge policy: unknown field %q", key)
		}
	}
	if refresh, exists := fields["refresh"]; exists {
		r, ok := refresh.(map[string]any)
		if !ok || r["quiet_for_seconds"] == nil || r["max_wait_seconds"] == nil {
			return KnowledgeSourcePolicy{}, fmt.Errorf("normalized refresh requires quiet_for_seconds and max_wait_seconds")
		}
	}
	if processing, exists := fields["processing"]; exists {
		if _, ok := processing.(map[string]any); !ok {
			return KnowledgeSourcePolicy{}, fmt.Errorf("knowledge processing must be an object")
		}
	}
	var policy KnowledgeSourcePolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if files, exists := fields["files"]; exists {
		raw, _ := json.Marshal(files)
		folderPolicy, err := filepolicy.DecodeFolderPolicy(raw)
		if err != nil {
			return policy, err
		}
		policy.Files = &folderPolicy
	}
	if r := policy.Refresh; r != nil {
		if r.QuietForSeconds < 0 || r.MaxWaitSeconds <= 0 || r.MaxWaitSeconds < r.QuietForSeconds || r.MaxWaitSeconds > 86400 {
			return policy, fmt.Errorf("invalid normalized knowledge refresh policy")
		}
	}
	if p := policy.Processing; p != nil {
		if err := ValidateKnowledgeProcessingPolicy(*p); err != nil {
			return policy, err
		}
	}
	if w := policy.Workspace; w != nil {
		if err := ValidateNotesWorkspacePath(w.Path); err != nil {
			return policy, err
		}
		if len(w.Include) == 0 {
			return policy, fmt.Errorf("normalized workspace requires include patterns")
		}
		if err := watchedroots.ValidatePatterns(w.Include); err != nil {
			return policy, err
		}
		if err := watchedroots.ValidatePatterns(w.Exclude); err != nil {
			return policy, err
		}
	}
	return policy, nil
}

func ValidateNotesWorkspacePath(value string) error {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "/")
	if len(value) > 1024 || !utf8.ValidString(value) || norm.NFC.String(value) != value || path.Clean(value) != value || len(parts) < 3 || parts[0] != "Projects" || parts[1] == "_Passive" || parts[1] == "_Archived" {
		return fmt.Errorf("knowledge.workspace.path: expected a canonical Projects/<project>/<folder> working root")
	}
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part || strings.HasPrefix(part, ".") || strings.ContainsAny(part, "\\:") || part == "credentials" || part == "private_no_index" {
			return fmt.Errorf("knowledge.workspace.path: invalid or private path component")
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return fmt.Errorf("knowledge.workspace.path: control character in path")
			}
		}
	}
	return nil
}

func ValidateKnowledgeProcessingPolicy(p KnowledgeProcessingPolicy) error {
	if p.OCR != "" && p.OCR != "auto" && p.OCR != "off" {
		return fmt.Errorf("knowledge.processing.ocr must be auto or off")
	}
	if err := watchedroots.ValidatePatterns(p.Paths); err != nil {
		return fmt.Errorf("knowledge.processing.paths: %w", err)
	}
	seen := map[string]bool{}
	for _, family := range p.EmbeddingFileTypes {
		switch family {
		case "markdown", "text", "pdf", "image":
		default:
			return fmt.Errorf("knowledge.processing.embedding_file_types: unsupported type %q", family)
		}
		if seen[family] {
			return fmt.Errorf("knowledge.processing.embedding_file_types: duplicate type %q", family)
		}
		seen[family] = true
	}
	return nil
}

func knowledgeDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 || duration > 24*time.Hour || duration%time.Second != 0 {
		return 0, fmt.Errorf("expected a whole-second duration from 0s through 24h")
	}
	return duration, nil
}
