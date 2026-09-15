package filesystemconnector

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

const (
	ErrorUnknownRoot        = "filesystem.unknown_root"
	ErrorPathEscapeDenied   = "filesystem.path_escape_denied"
	ErrorAbsolutePathDenied = "filesystem.absolute_path_denied"
	ErrorHiddenPathDenied   = "filesystem.hidden_path_denied"
	ErrorPrivateRootDenied  = "filesystem.private_root_denied"
	ErrorPermissionDenied   = "filesystem.permission_denied"
	ErrorNotFound           = "filesystem.not_found"
	ErrorNotFile            = "filesystem.not_file"
	ErrorNotDirectory       = "filesystem.not_directory"
	ErrorFileTooLarge       = "filesystem.file_too_large"

	defaultListMaxEntries = 50
	upperListMaxEntries   = 200
)

type PolicyError struct {
	Code    string
	Message string
}

func (e *PolicyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type SafeListInput struct {
	Root          string `json:"root"`
	Path          string `json:"path,omitempty"`
	MaxEntries    int    `json:"max_entries,omitempty"`
	IncludeHidden *bool  `json:"include_hidden,omitempty"`
}

type ReadMetadataInput struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

type IngestFileInput struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

type SafeListResult struct {
	Root      string              `json:"root"`
	Path      string              `json:"path"`
	Entries   []FilesystemEntry   `json:"entries"`
	Truncated bool                `json:"truncated"`
	RootInfo  SafeRootListSummary `json:"root_info"`
}

type SafeRootListSummary struct {
	RootKey     string `json:"root_key"`
	DisplayName string `json:"display_name"`
}

type FilesystemEntry struct {
	Name                string `json:"name"`
	RelativePath        string `json:"relative_path"`
	Kind                string `json:"kind"`
	SizeBytes           int64  `json:"size_bytes"`
	ModifiedAt          string `json:"modified_at"`
	SourceModifiedBasis string `json:"source_modified_basis"`
	SourceCreatedAt     string `json:"source_created_at,omitempty"`
	SourceCreatedBasis  string `json:"source_created_basis,omitempty"`
	Hidden              bool   `json:"hidden"`
}

type FileMetadataResult struct {
	Root                string `json:"root"`
	Path                string `json:"path"`
	Kind                string `json:"kind"`
	SizeBytes           int64  `json:"size_bytes"`
	ModifiedAt          string `json:"modified_at"`
	SourceModifiedBasis string `json:"source_modified_basis"`
	SourceCreatedAt     string `json:"source_created_at,omitempty"`
	SourceCreatedBasis  string `json:"source_created_basis,omitempty"`
	MimeType            string `json:"mime_type,omitempty"`
	Hidden              bool   `json:"hidden"`
	IngestAllowed       bool   `json:"ingest_allowed"`
}

type PreparedIngest struct {
	RootKey            string
	RelativePath       string
	LogicalPath        string
	AbsolutePath       string
	LogicalName        string
	SourceMtime        time.Time
	SourceMtimeBasis   string
	SourceCreatedAt    *time.Time
	SourceCreatedBasis string
	SizeBytes          int64
	MimeType           string
	HashURI            string
	Content            []byte
}

type resolvedPath struct {
	Root         SafeRoot
	RelativePath string
	AbsolutePath string
	Info         os.FileInfo
	Hidden       bool
}

func SafeList(config Config, input SafeListInput) (SafeListResult, error) {
	root, includeHidden, err := rootForOperation(config, input.Root, "list")
	if err != nil {
		return SafeListResult{}, err
	}
	if input.IncludeHidden != nil {
		includeHidden = *input.IncludeHidden
	}
	resolved, err := resolveExistingPath(root, input.Path, includeHidden)
	if err != nil {
		return SafeListResult{}, err
	}
	if !resolved.Info.IsDir() {
		return SafeListResult{}, policyError(ErrorNotDirectory, "requested path is not a directory")
	}

	entries, err := os.ReadDir(resolved.AbsolutePath)
	if err != nil {
		return SafeListResult{}, policyError(ErrorNotDirectory, "requested directory could not be read")
	}
	out := make([]FilesystemEntry, 0, len(entries))
	for _, entry := range entries {
		hidden := isHiddenName(entry.Name())
		if hidden && !includeHidden {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		relativePath := joinRelativePath(resolved.RelativePath, entry.Name())
		_, sourceCreatedAt, sourceCreatedBasis := filesystemSourceTimestamps(filepath.Join(resolved.AbsolutePath, entry.Name()), resolved.AbsolutePath, info)
		out = append(out, FilesystemEntry{
			Name:                entry.Name(),
			RelativePath:        relativePath,
			Kind:                fileKind(info),
			SizeBytes:           info.Size(),
			ModifiedAt:          info.ModTime().UTC().Format(time.RFC3339),
			SourceModifiedBasis: filesystemmeta.SourceTimeBasisFilesystemMtime,
			SourceCreatedAt:     optionalFilesystemTime(sourceCreatedAt),
			SourceCreatedBasis:  sourceCreatedBasis,
			Hidden:              hidden,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelativePath < out[j].RelativePath
	})

	maxEntries := input.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultListMaxEntries
	}
	if maxEntries > upperListMaxEntries {
		maxEntries = upperListMaxEntries
	}
	truncated := len(out) > maxEntries
	if truncated {
		out = out[:maxEntries]
	}

	return SafeListResult{
		Root:      root.RootKey,
		Path:      resolved.RelativePath,
		Entries:   out,
		Truncated: truncated,
		RootInfo: SafeRootListSummary{
			RootKey:     root.RootKey,
			DisplayName: root.DisplayName,
		},
	}, nil
}

func ReadMetadata(config Config, input ReadMetadataInput) (FileMetadataResult, error) {
	root, includeHidden, err := rootForOperation(config, input.Root, "metadata")
	if err != nil {
		return FileMetadataResult{}, err
	}
	resolved, err := resolveExistingPath(root, input.Path, includeHidden)
	if err != nil {
		return FileMetadataResult{}, err
	}
	mimeType := ""
	if resolved.Info.Mode().IsRegular() {
		mimeType = detectFileMIME(resolved.AbsolutePath)
	}
	_, sourceCreatedAt, sourceCreatedBasis := filesystemSourceTimestamps(resolved.AbsolutePath, root.AbsolutePath, resolved.Info)
	return FileMetadataResult{
		Root:                root.RootKey,
		Path:                resolved.RelativePath,
		Kind:                fileKind(resolved.Info),
		SizeBytes:           resolved.Info.Size(),
		ModifiedAt:          resolved.Info.ModTime().UTC().Format(time.RFC3339),
		SourceModifiedBasis: filesystemmeta.SourceTimeBasisFilesystemMtime,
		SourceCreatedAt:     optionalFilesystemTime(sourceCreatedAt),
		SourceCreatedBasis:  sourceCreatedBasis,
		MimeType:            mimeType,
		Hidden:              resolved.Hidden,
		IngestAllowed:       root.AllowIngest && resolved.Info.Mode().IsRegular() && resolved.Info.Size() <= root.MaxFileBytes,
	}, nil
}

func PrepareIngest(config Config, input IngestFileInput) (PreparedIngest, error) {
	root, includeHidden, err := rootForOperation(config, input.Root, "ingest")
	if err != nil {
		return PreparedIngest{}, err
	}
	resolved, err := resolveExistingPath(root, input.Path, includeHidden)
	if err != nil {
		return PreparedIngest{}, err
	}
	if !resolved.Info.Mode().IsRegular() {
		return PreparedIngest{}, policyError(ErrorNotFile, "requested path is not a regular file")
	}
	if resolved.Info.Size() > root.MaxFileBytes {
		return PreparedIngest{}, policyError(ErrorFileTooLarge, "requested file exceeds this safe root size limit")
	}
	content, err := os.ReadFile(resolved.AbsolutePath)
	if err != nil {
		return PreparedIngest{}, policyError(ErrorNotFile, "requested file could not be read")
	}
	if int64(len(content)) > root.MaxFileBytes {
		return PreparedIngest{}, policyError(ErrorFileTooLarge, "requested file exceeds this safe root size limit")
	}
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])
	sourceMtime, sourceCreatedAt, sourceCreatedBasis := filesystemSourceTimestamps(resolved.AbsolutePath, root.AbsolutePath, resolved.Info)
	return PreparedIngest{
		RootKey:            root.RootKey,
		RelativePath:       resolved.RelativePath,
		LogicalPath:        "filesystem://" + root.RootKey + "/" + resolved.RelativePath,
		AbsolutePath:       resolved.AbsolutePath,
		LogicalName:        filepath.Base(resolved.RelativePath),
		SourceMtime:        sourceMtime,
		SourceMtimeBasis:   filesystemmeta.SourceTimeBasisFilesystemMtime,
		SourceCreatedAt:    sourceCreatedAt,
		SourceCreatedBasis: sourceCreatedBasis,
		SizeBytes:          int64(len(content)),
		MimeType:           detectMIMEFromContent(resolved.AbsolutePath, content),
		HashURI:            "sha256:" + hashHex,
		Content:            content,
	}, nil
}

func filesystemSourceTimestamps(path, root string, info os.FileInfo) (time.Time, *time.Time, string) {
	modifiedAt := info.ModTime().UTC()
	observation, err := filesystemmeta.DetectPath(path, filesystemmeta.DetectOptions{RootPath: root})
	if err != nil {
		return modifiedAt, nil, ""
	}
	if observation.SourceModifiedAt != nil {
		modifiedAt = observation.SourceModifiedAt.UTC()
	}
	return modifiedAt, observation.SourceCreatedAt, observation.SourceCreatedBasis
}

func optionalFilesystemTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func PolicyErrorCode(err error) string {
	var policyErr *PolicyError
	if errors.As(err, &policyErr) {
		return policyErr.Code
	}
	return "filesystem.execution_failed"
}

func PolicyErrorMessage(err error) string {
	var policyErr *PolicyError
	if errors.As(err, &policyErr) {
		return policyErr.Message
	}
	return "filesystem connector operation failed"
}

func CanonicalSafeRootPath(root SafeRoot) (string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root.AbsolutePath)
	if err != nil {
		return "", policyError(ErrorNotDirectory, "safe root is not reachable")
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil {
		return "", policyError(ErrorNotDirectory, "safe root could not be inspected")
	}
	if !info.IsDir() {
		return "", policyError(ErrorNotDirectory, "safe root is not a directory")
	}
	return filepath.Clean(canonicalRoot), nil
}

func NormalizeRelativePath(raw string) (string, bool, error) {
	return normalizeRelativePath(raw)
}

func IsWithin(rootPath, targetPath string) bool {
	return isWithin(rootPath, targetPath)
}

func rootForOperation(config Config, rootKey string, operation string) (SafeRoot, bool, error) {
	config = NormalizeConfig(config)
	key := NormalizeRootKey(rootKey)
	if key == "" {
		return SafeRoot{}, false, policyError(ErrorUnknownRoot, "safe root is required")
	}
	for _, root := range config.SafeRoots {
		if root.RootKey != key {
			continue
		}
		if root.PrivateBackupOnly {
			return SafeRoot{}, false, policyError(ErrorPrivateRootDenied, "safe root is private backup-only")
		}
		switch operation {
		case "list":
			if !root.AllowList {
				return SafeRoot{}, false, policyError(ErrorPermissionDenied, "safe root does not allow listing")
			}
		case "metadata":
			if !root.AllowMetadata {
				return SafeRoot{}, false, policyError(ErrorPermissionDenied, "safe root does not allow metadata reads")
			}
		case "ingest":
			if !root.AllowIngest {
				return SafeRoot{}, false, policyError(ErrorPermissionDenied, "safe root does not allow ingest")
			}
		}
		return root, root.IncludeHiddenDefault, nil
	}
	return SafeRoot{}, false, policyError(ErrorUnknownRoot, "safe root is not configured")
}

func resolveExistingPath(root SafeRoot, rawRelativePath string, includeHidden bool) (resolvedPath, error) {
	relativePath, hidden, err := normalizeRelativePath(rawRelativePath)
	if err != nil {
		return resolvedPath{}, err
	}
	if hidden && !includeHidden {
		return resolvedPath{}, policyError(ErrorHiddenPathDenied, "hidden paths are not visible through this safe root")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.AbsolutePath)
	if err != nil {
		return resolvedPath{}, policyError(ErrorNotDirectory, "safe root is not reachable")
	}
	targetPath := filepath.Join(canonicalRoot, filepath.FromSlash(relativePath))
	canonicalTarget, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedPath{}, policyError(ErrorNotFound, "requested path was not found")
		}
		return resolvedPath{}, policyError(ErrorPathEscapeDenied, "requested path could not be safely resolved")
	}
	if !isWithin(canonicalRoot, canonicalTarget) {
		return resolvedPath{}, policyError(ErrorPathEscapeDenied, "requested path escapes the safe root")
	}
	info, err := os.Stat(canonicalTarget)
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedPath{}, policyError(ErrorNotFound, "requested path was not found")
		}
		return resolvedPath{}, policyError(ErrorPathEscapeDenied, "requested path could not be inspected")
	}
	return resolvedPath{
		Root:         root,
		RelativePath: relativePath,
		AbsolutePath: canonicalTarget,
		Info:         info,
		Hidden:       hidden,
	}, nil
}

func normalizeRelativePath(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "."
	}
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "/") || filepath.IsAbs(value) {
		return "", false, policyError(ErrorAbsolutePathDenied, "absolute paths are not accepted")
	}
	parts := strings.Split(value, "/")
	hidden := false
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", false, policyError(ErrorPathEscapeDenied, "parent directory traversal is not accepted")
		}
		if isHiddenName(part) {
			hidden = true
		}
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned == "" {
		cleaned = "."
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false, policyError(ErrorPathEscapeDenied, "parent directory traversal is not accepted")
	}
	return cleaned, hidden, nil
}

func isWithin(rootPath, targetPath string) bool {
	relative, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return false
	}
	return relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..")
}

func joinRelativePath(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(base), name))
}

func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

func fileKind(info os.FileInfo) string {
	mode := info.Mode()
	switch {
	case mode.IsRegular():
		return "file"
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

func detectFileMIME(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return mimeFromExtension(path)
	}
	defer file.Close()
	buffer := make([]byte, 512)
	n, _ := io.ReadFull(file, buffer)
	if n <= 0 {
		return mimeFromExtension(path)
	}
	return detectMIMEFromContent(path, buffer[:n])
}

func detectMIMEFromContent(path string, content []byte) string {
	if mimeType := mimeFromExtension(path); mimeType != "" {
		return mimeType
	}
	if len(content) == 0 {
		return "application/octet-stream"
	}
	sample := content
	if len(sample) > 512 {
		sample = sample[:512]
	}
	return http.DetectContentType(sample)
}

func mimeFromExtension(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	default:
		return ""
	}
}

func policyError(code string, message string) error {
	return &PolicyError{Code: code, Message: message}
}

func IsPolicyError(err error) bool {
	var policyErr *PolicyError
	return errors.As(err, &policyErr)
}

func ValidatePreparedIngestForInlineUpload(prepared PreparedIngest, maxBytes int64) error {
	if prepared.SizeBytes > maxBytes {
		return policyError(ErrorFileTooLarge, fmt.Sprintf("requested file exceeds the %d byte inline upload limit", maxBytes))
	}
	return nil
}
