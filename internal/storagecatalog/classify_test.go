package storagecatalog

import "testing"

func TestClassifyPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		mimeType string
		want     string
	}{
		{name: "markdown extension", path: "Notes/Idea.md", want: FileClassMarkdown},
		{name: "markdown mime", path: "Notes/Idea", mimeType: "text/markdown", want: FileClassMarkdown},
		{name: "plain text mime", path: "Documents/readme", mimeType: "text/plain", want: FileClassText},
		{name: "pdf", path: "Documents/Paper.pdf", want: FileClassPDF},
		{name: "docx", path: "Notes/Research.docx", want: FileClassOfficeDocument},
		{name: "legacy doc", path: "Notes/Archive.DOC", want: FileClassOfficeDocument},
		{name: "odt", path: "Notes/Draft.odt", want: FileClassOfficeDocument},
		{name: "rtf", path: "Notes/Clipping.rtf", want: FileClassOfficeDocument},
		{name: "google docs pointer", path: "Notes/Cloud Note.gdoc", want: FileClassOfficeDocument},
		{name: "image", path: "Images/photo.HEIC", want: FileClassImage},
		{name: "video", path: "Videos/clip.mov", want: FileClassVideo},
		{name: "audio", path: "Audio/interview.wav", want: FileClassAudio},
		{name: "archive", path: "Backups/data.tar", want: FileClassArchive},
		{name: "code", path: "Repos/main.go", want: FileClassCode},
		{name: "toml structured data", path: "Notes/config.toml", want: FileClassCode},
		{name: "binary", path: "bin/data.bin", want: FileClassBinary},
		{name: "directory", path: "Documents/folder/", want: FileClassDirectory},
		{name: "unknown extensionless", path: "Documents/LICENSE", want: FileClassUnknown},
		{name: "generated apple metadata", path: "Documents/.DS_Store", want: FileClassGeneratedMetadata},
		{name: "apple double sidecar", path: "Documents/._note.md", want: FileClassGeneratedMetadata},
		{name: "loom metadata sidecar", path: "Documents/note.md.loom-meta.json", want: FileClassGeneratedMetadata},
		{name: "generated dependency tree", path: "Repos/app/node_modules/pkg/index.js", want: FileClassGeneratedMetadata},
		{name: "package directory", path: "Documents/report.pages/", want: FileClassPackage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyPath(tt.path, tt.mimeType); got != tt.want {
				t.Fatalf("ClassifyPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyContentSniffing(t *testing.T) {
	tests := []struct {
		name       string
		input      ClassificationInput
		wantClass  string
		wantSource string
		wantState  string
	}{
		{
			name: "extensionless text",
			input: ClassificationInput{
				Path:          "Documents/LICENSE",
				ContentSample: []byte("Permission is hereby granted.\n"),
				SizeBytes:     31,
			},
			wantClass:  FileClassText,
			wantSource: ClassificationSourceContentSniff,
			wantState:  IndexingStateIndexed,
		},
		{
			name: "extensionless markdown",
			input: ClassificationInput{
				Path:          "Documents/README",
				ContentSample: []byte("# Project\n\n- item\n"),
				SizeBytes:     17,
			},
			wantClass:  FileClassMarkdown,
			wantSource: ClassificationSourceContentSniff,
			wantState:  IndexingStateIndexed,
		},
		{
			name: "extensionless shebang script",
			input: ClassificationInput{
				Path:          "bin/deploy",
				ContentSample: []byte("#!/usr/bin/env bash\nset -e\n"),
				SizeBytes:     27,
			},
			wantClass:  FileClassCode,
			wantSource: ClassificationSourceShebang,
			wantState:  IndexingStateIndexed,
		},
		{
			name: "extensionless binary",
			input: ClassificationInput{
				Path:          "bin/blob",
				ContentSample: []byte{0x00, 0x01, 0x02},
				SizeBytes:     3,
			},
			wantClass:  FileClassBinary,
			wantSource: ClassificationSourceContentSniff,
			wantState:  IndexingStateBinary,
		},
		{
			name: "large markdown above index cap",
			input: ClassificationInput{
				Path:          "Notes/long.md",
				ContentSample: []byte("# Long\n"),
				SizeBytes:     DefaultMaxIndexBytes + 1,
			},
			wantClass:  FileClassMarkdown,
			wantSource: ClassificationSourceExtension,
			wantState:  IndexingStateTooLarge,
		},
		{
			name: "zero byte markdown",
			input: ClassificationInput{
				Path:      "Notes/empty.md",
				SizeBytes: 0,
			},
			wantClass:  FileClassMarkdown,
			wantSource: ClassificationSourceExtension,
			wantState:  IndexingStateIndexed,
		},
		{
			name: "permission denied",
			input: ClassificationInput{
				Path:             "Notes/secret.md",
				PermissionDenied: true,
			},
			wantClass:  FileClassUnknown,
			wantSource: ClassificationSourceManualPolicy,
			wantState:  IndexingStatePermissionDenied,
		},
		{
			name: "generated build output ignored",
			input: ClassificationInput{
				Path:      "Repos/app/dist/bundle.js",
				SizeBytes: 1024,
			},
			wantClass:  FileClassGeneratedMetadata,
			wantSource: ClassificationSourceGeneratedMetadata,
			wantState:  IndexingStateGeneratedIgnored,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.input)
			if got.FileClass != tt.wantClass ||
				got.ClassificationSource != tt.wantSource ||
				got.IndexingState != tt.wantState {
				t.Fatalf("Classify() = %#v, want class=%q source=%q state=%q", got, tt.wantClass, tt.wantSource, tt.wantState)
			}
		})
	}
}

func TestIsTextIndexCandidate(t *testing.T) {
	for _, class := range []string{FileClassMarkdown, FileClassText, FileClassCode} {
		if !IsTextIndexCandidate(class) {
			t.Fatalf("%q should be a text index candidate", class)
		}
	}
	if IsTextIndexCandidate(FileClassVideo) {
		t.Fatal("video should not be a text index candidate")
	}
}
