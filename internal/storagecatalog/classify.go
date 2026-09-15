package storagecatalog

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

const DefaultMaxIndexBytes = 512 * 1024

var codeExtensions = map[string]bool{
	".bash": true,
	".c":    true,
	".cc":   true,
	".cpp":  true,
	".css":  true,
	".go":   true,
	".h":    true,
	".hpp":  true,
	".html": true,
	".java": true,
	".js":   true,
	".jsx":  true,
	".json": true,
	".lua":  true,
	".mjs":  true,
	".py":   true,
	".rb":   true,
	".rs":   true,
	".sh":   true,
	".sql":  true,
	".ts":   true,
	".tsx":  true,
	".toml": true,
	".xml":  true,
	".yaml": true,
	".yml":  true,
}

var textExtensions = map[string]bool{
	".adoc": true,
	".csv":  true,
	".log":  true,
	".rst":  true,
	".text": true,
	".txt":  true,
	".tsv":  true,
}

var imageExtensions = map[string]bool{
	".avif": true,
	".gif":  true,
	".heic": true,
	".jpeg": true,
	".jpg":  true,
	".png":  true,
	".svg":  true,
	".tif":  true,
	".tiff": true,
	".webp": true,
}

var videoExtensions = map[string]bool{
	".avi":  true,
	".m4v":  true,
	".mkv":  true,
	".mov":  true,
	".mp4":  true,
	".mpeg": true,
	".mpg":  true,
	".webm": true,
}

var audioExtensions = map[string]bool{
	".aac":  true,
	".aiff": true,
	".flac": true,
	".m4a":  true,
	".mp3":  true,
	".ogg":  true,
	".wav":  true,
}

var archiveExtensions = map[string]bool{
	".7z":  true,
	".bz2": true,
	".gz":  true,
	".rar": true,
	".tar": true,
	".tgz": true,
	".xz":  true,
	".zip": true,
}

var officeDocumentExtensions = map[string]bool{
	".doc":  true,
	".docx": true,
	".gdoc": true,
	".odt":  true,
	".rtf":  true,
}

var packageExtensions = map[string]bool{
	".app":           true,
	".bundle":        true,
	".framework":     true,
	".key":           true,
	".logicx":        true,
	".numbers":       true,
	".pages":         true,
	".photoslibrary": true,
	".playground":    true,
	".rtfd":          true,
	".scriv":         true,
	".xcarchive":     true,
	".xcworkspace":   true,
	".xcodeproj":     true,
}

type ClassificationInput struct {
	Path              string
	MimeType          string
	ContentSample     []byte
	IsDirectory       bool
	IsPackage         bool
	GeneratedMetadata bool
	PermissionDenied  bool
	SizeBytes         int64
	MaxIndexBytes     int64
	ManualPolicy      string
}

type ClassificationResult struct {
	FileClass            string `json:"file_class"`
	ClassificationSource string `json:"classification_source"`
	IndexingState        string `json:"indexing_state"`
	Reason               string `json:"reason,omitempty"`
}

func ClassifyPath(pathValue, mimeType string) string {
	return Classify(ClassificationInput{Path: pathValue, MimeType: mimeType}).FileClass
}

func Classify(input ClassificationInput) ClassificationResult {
	pathValue := strings.TrimSpace(input.Path)
	mimeType := strings.ToLower(strings.TrimSpace(input.MimeType))
	if input.MaxIndexBytes <= 0 {
		input.MaxIndexBytes = DefaultMaxIndexBytes
	}

	base := filepath.Base(filepath.Clean(pathValue))
	if input.GeneratedMetadata || filesystemmeta.IsGeneratedAppleMetadata(base) {
		return ClassificationResult{
			FileClass:            FileClassGeneratedMetadata,
			ClassificationSource: ClassificationSourceGeneratedMetadata,
			IndexingState:        IndexingStateGeneratedIgnored,
			Reason:               "generated Apple metadata is excluded from rich indexing",
		}
	}
	if filepolicy.IsIndexingExcludedPath(pathValue) {
		return ClassificationResult{
			FileClass:            FileClassGeneratedMetadata,
			ClassificationSource: ClassificationSourceGeneratedMetadata,
			IndexingState:        IndexingStateGeneratedIgnored,
			Reason:               "generated dependency/runtime path is excluded from rich indexing",
		}
	}
	if input.PermissionDenied {
		return ClassificationResult{
			FileClass:            FileClassUnknown,
			ClassificationSource: ClassificationSourceManualPolicy,
			IndexingState:        IndexingStatePermissionDenied,
			Reason:               "file could not be inspected due to permissions",
		}
	}

	pathValue = strings.TrimSpace(pathValue)
	if input.IsPackage || (input.IsDirectory || strings.HasSuffix(pathValue, "/")) && packageExtensions[strings.ToLower(filepath.Ext(strings.TrimSuffix(pathValue, "/")))] {
		return ClassificationResult{
			FileClass:            FileClassPackage,
			ClassificationSource: ClassificationSourcePackageExtension,
			IndexingState:        IndexingStateMetadataOnly,
			Reason:               "package directories are tracked as metadata boundaries unless explicitly descended",
		}
	}
	if input.IsDirectory || strings.HasSuffix(pathValue, "/") {
		return ClassificationResult{
			FileClass:            FileClassDirectory,
			ClassificationSource: ClassificationSourceExtension,
			IndexingState:        IndexingStateMetadataOnly,
		}
	}

	ext := strings.ToLower(filepath.Ext(pathValue))
	switch {
	case ext == ".md" || ext == ".markdown" || ext == ".mdown":
		return resultWithIndexState(FileClassMarkdown, ClassificationSourceExtension, input.SizeBytes, input.MaxIndexBytes)
	case ext == ".pdf":
		return ClassificationResult{FileClass: FileClassPDF, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateMetadataOnly}
	case officeDocumentExtensions[ext]:
		return ClassificationResult{FileClass: FileClassOfficeDocument, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateMetadataOnly}
	case textExtensions[ext]:
		return resultWithIndexState(FileClassText, ClassificationSourceExtension, input.SizeBytes, input.MaxIndexBytes)
	case codeExtensions[ext]:
		return resultWithIndexState(FileClassCode, ClassificationSourceExtension, input.SizeBytes, input.MaxIndexBytes)
	case imageExtensions[ext]:
		return ClassificationResult{FileClass: FileClassImage, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateBinary}
	case videoExtensions[ext]:
		return ClassificationResult{FileClass: FileClassVideo, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateBinary}
	case audioExtensions[ext]:
		return ClassificationResult{FileClass: FileClassAudio, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateBinary}
	case archiveExtensions[ext]:
		return ClassificationResult{FileClass: FileClassArchive, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateBinary}
	}

	if strings.HasPrefix(mimeType, "text/") {
		if strings.Contains(mimeType, "markdown") {
			return resultWithIndexState(FileClassMarkdown, ClassificationSourceMIME, input.SizeBytes, input.MaxIndexBytes)
		}
		return resultWithIndexState(FileClassText, ClassificationSourceMIME, input.SizeBytes, input.MaxIndexBytes)
	}
	if strings.Contains(mimeType, "pdf") {
		return ClassificationResult{FileClass: FileClassPDF, ClassificationSource: ClassificationSourceMIME, IndexingState: IndexingStateMetadataOnly}
	}
	if strings.HasPrefix(mimeType, "image/") {
		return ClassificationResult{FileClass: FileClassImage, ClassificationSource: ClassificationSourceMIME, IndexingState: IndexingStateBinary}
	}
	if strings.HasPrefix(mimeType, "video/") {
		return ClassificationResult{FileClass: FileClassVideo, ClassificationSource: ClassificationSourceMIME, IndexingState: IndexingStateBinary}
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return ClassificationResult{FileClass: FileClassAudio, ClassificationSource: ClassificationSourceMIME, IndexingState: IndexingStateBinary}
	}
	if ext == "" {
		return classifyExtensionless(input)
	}
	return ClassificationResult{FileClass: FileClassBinary, ClassificationSource: ClassificationSourceExtension, IndexingState: IndexingStateBinary}
}

func IsTextIndexCandidate(fileClass string) bool {
	return fileClass == FileClassMarkdown || fileClass == FileClassText || fileClass == FileClassCode
}

func resultWithIndexState(fileClass, source string, sizeBytes, maxIndexBytes int64) ClassificationResult {
	if maxIndexBytes <= 0 {
		maxIndexBytes = DefaultMaxIndexBytes
	}
	result := ClassificationResult{
		FileClass:            fileClass,
		ClassificationSource: source,
		IndexingState:        IndexingStateIndexed,
	}
	if sizeBytes > maxIndexBytes {
		result.IndexingState = IndexingStateTooLarge
		result.Reason = "text-like file exceeds rich indexing size cap"
	}
	return result
}

func classifyExtensionless(input ClassificationInput) ClassificationResult {
	sample := input.ContentSample
	if sample == nil && input.SizeBytes == 0 {
		return ClassificationResult{FileClass: FileClassUnknown, ClassificationSource: ClassificationSourceContentSniff, IndexingState: IndexingStateUnsupported}
	}
	if len(sample) == 0 && input.SizeBytes == 0 {
		return resultWithIndexState(FileClassText, ClassificationSourceContentSniff, 0, input.MaxIndexBytes)
	}
	if bytes.HasPrefix(sample, []byte("#!")) {
		return resultWithIndexState(FileClassCode, ClassificationSourceShebang, input.SizeBytes, input.MaxIndexBytes)
	}
	if isBinarySample(sample) {
		return ClassificationResult{FileClass: FileClassBinary, ClassificationSource: ClassificationSourceContentSniff, IndexingState: IndexingStateBinary}
	}
	if isMarkdownLike(sample) {
		return resultWithIndexState(FileClassMarkdown, ClassificationSourceContentSniff, input.SizeBytes, input.MaxIndexBytes)
	}
	if isTextSample(sample) {
		return resultWithIndexState(FileClassText, ClassificationSourceContentSniff, input.SizeBytes, input.MaxIndexBytes)
	}
	return ClassificationResult{FileClass: FileClassUnknown, ClassificationSource: ClassificationSourceContentSniff, IndexingState: IndexingStateUnsupported}
}

func isBinarySample(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	return bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(sample)
}

func isTextSample(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if !utf8.Valid(sample) {
		return false
	}
	control := 0
	for _, r := range string(sample) {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			control++
		}
	}
	return control*100/len([]rune(string(sample))) < 5
}

func isMarkdownLike(sample []byte) bool {
	text := strings.TrimSpace(string(sample))
	if text == "" {
		return false
	}
	return strings.HasPrefix(text, "# ") ||
		strings.HasPrefix(text, "## ") ||
		strings.HasPrefix(text, "---\n") ||
		strings.Contains(text, "](") ||
		strings.Contains(text, "\n- ") ||
		strings.Contains(text, "\n* ")
}
