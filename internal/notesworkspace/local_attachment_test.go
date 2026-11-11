package notesworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type localAttachmentFixture struct {
	root   string
	source string
	data   []byte
	r      LocalClientRequest
}

func newLocalAttachmentFixture(t *testing.T, data []byte) localAttachmentFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Attachments"), 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(staging, "private-staging.bin")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	return localAttachmentFixture{root: root, source: source, data: data, r: LocalClientRequest{
		RootIdentity: fmt.Sprintf("%d:%d", st.Dev, st.Ino), ID: "attachment-1", Kind: "import_attachment",
		Path: "Attachments/picture.png", Source: source, SHA256: digest(data), Size: int64(len(data)),
	}}
}

func (f localAttachmentFixture) target() string { return filepath.Join(f.root, f.r.Path) }
func (f localAttachmentFixture) journal() string {
	return filepath.Join(filepath.Dir(f.target()), ".loom-notes-"+identity("mac-"+f.r.ID))
}

func attachmentRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func attachmentStat(t *testing.T, path string) MetadataActionIdentity {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	st, err := localAttachmentStat(file)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func attachmentAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected absent %s, got %v", path, err)
	}
}

func attachmentApply(t *testing.T, f localAttachmentFixture) {
	t.Helper()
	if err := ApplyLocalClient(f.root, f.r); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAttachmentImportBinaryAndReplay(t *testing.T) {
	for _, name := range []string{"empty", "binary", "bare-uppercase-digest"} {
		t.Run(name, func(t *testing.T) {
			var data []byte
			if name != "empty" {
				data = bytes.Repeat([]byte{0, 0xff, 0xfe, '\r', '\n', 0x80, 'a'}, 25000)
			}
			f := newLocalAttachmentFixture(t, data)
			if name == "bare-uppercase-digest" {
				f.r.SHA256 = strings.ToUpper(strings.TrimPrefix(f.r.SHA256, "sha256:"))
			}
			before := attachmentStat(t, f.source)
			attachmentApply(t, f)
			if !bytes.Equal(attachmentRead(t, f.target()), data) {
				t.Fatal("attachment bytes changed")
			}
			published := attachmentStat(t, f.target())
			if published.inode() == before.inode() {
				t.Fatal("source inode was moved or linked instead of copied")
			}
			var prepared localAttachmentPrepared
			if err := json.Unmarshal(attachmentRead(t, filepath.Join(f.journal(), "attachment-prepared.json")), &prepared); err != nil {
				t.Fatal(err)
			}
			if !prepared.File.matchesMoved(&published) {
				t.Fatal("publication does not match durable prepared-inode receipt")
			}
			if string(attachmentRead(t, filepath.Join(f.journal(), "complete"))) != f.r.Path {
				t.Fatal("completion receipt missing")
			}
			attachmentApply(t, f)
			// Equivalent digest forms bind the same immutable intent.
			f.r.SHA256 = strings.TrimPrefix(digest(data), "sha256:")
			attachmentApply(t, f)
			if after := attachmentStat(t, f.target()); after != published {
				t.Fatal("replay replaced or changed the published file")
			}
			if after := attachmentStat(t, f.source); after != before || !bytes.Equal(attachmentRead(t, f.source), data) {
				t.Fatal("source changed during import or replay")
			}
			attachmentAbsent(t, filepath.Join(f.journal(), "attachment"))
		})
	}
}

func TestLocalAttachmentRejectsWrongHashAndSize(t *testing.T) {
	tests := []struct {
		name   string
		change func(*LocalClientRequest)
	}{
		{"wrong-hash", func(r *LocalClientRequest) { r.SHA256 = digest([]byte("different")) }},
		{"non-hex-hash", func(r *LocalClientRequest) { r.SHA256 = strings.Repeat("z", 64) }},
		{"short-hash", func(r *LocalClientRequest) { r.SHA256 = "aabb" }},
		{"empty-hash", func(r *LocalClientRequest) { r.SHA256 = "" }},
		{"short-size", func(r *LocalClientRequest) { r.Size-- }},
		{"long-size", func(r *LocalClientRequest) { r.Size++ }},
		{"negative-size", func(r *LocalClientRequest) { r.Size = -1 }},
		{"too-large", func(r *LocalClientRequest) { r.Size = maxLocalAttachmentBytes + 1 }},
		{"unexpected-text", func(r *LocalClientRequest) { r.Text = "not a text save" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 0xff, 1, 2})
			before := attachmentStat(t, f.source)
			tt.change(&f.r)
			if err := ApplyLocalClient(f.root, f.r); err == nil {
				t.Fatal("invalid request accepted")
			}
			attachmentAbsent(t, f.target())
			if attachmentStat(t, f.source) != before || !bytes.Equal(attachmentRead(t, f.source), f.data) {
				t.Fatal("rejection changed source")
			}
			if tt.name == "wrong-hash" {
				entries, err := os.ReadDir(f.journal())
				if err != nil {
					t.Fatal(err)
				}
				retained := false
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), ".staged-attachment-") {
						retained = bytes.Equal(attachmentRead(t, filepath.Join(f.journal(), e.Name())), f.data)
					}
				}
				if !retained {
					t.Fatal("failed hash staging/history was discarded")
				}
				attachmentAbsent(t, filepath.Join(f.journal(), "attachment-prepared.json"))
			}
		})
	}
	for _, size := range []int64{0, maxLocalAttachmentBytes} {
		r := newLocalAttachmentFixture(t, nil).r
		r.Size = size
		if err := validateLocalAttachmentRequest(&r); err != nil {
			t.Fatalf("size boundary %d rejected: %v", size, err)
		}
	}
}

func TestLocalAttachmentCollisionNeverAdoptsMatchingContent(t *testing.T) {
	f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
	if err := os.WriteFile(f.target(), f.data, 0600); err != nil {
		t.Fatal(err)
	}
	before := attachmentStat(t, f.target())
	if err := ApplyLocalClient(f.root, f.r); err == nil {
		t.Fatal("unrelated same-content collision accepted")
	}
	if attachmentStat(t, f.target()) != before || !bytes.Equal(attachmentRead(t, f.target()), f.data) {
		t.Fatal("collision was changed")
	}
	attachmentAbsent(t, filepath.Join(f.journal(), "attachment-prepared.json"))
}

func TestLocalAttachmentReplayRejectsReplacedOrEditedPublication(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace-%t", replace), func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
			attachmentApply(t, f)
			if replace {
				if err := os.Rename(f.target(), f.target()+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.target(), f.data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(f.target(), []byte{0, 2, 0xff}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := attachmentStat(t, f.target())
			content := attachmentRead(t, f.target())
			if err := ApplyLocalClient(f.root, f.r); err == nil {
				t.Fatal("replaced/edited publication accepted as replay")
			}
			if attachmentStat(t, f.target()) != before || !bytes.Equal(attachmentRead(t, f.target()), content) {
				t.Fatal("replay changed the external file")
			}
		})
	}
}

func TestLocalAttachmentResumesPreparedAndPublishedInterruptions(t *testing.T) {
	for _, phase := range []string{"staged", "prepared", "published"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 0xfe, 1, 0xff})
			attachmentApply(t, f)
			preparedPath := filepath.Join(f.journal(), "attachment-prepared.json")
			receipt := attachmentRead(t, preparedPath)
			published := attachmentStat(t, f.target())
			if err := os.Remove(filepath.Join(f.journal(), "complete")); err != nil {
				t.Fatal(err)
			}
			if phase != "published" {
				if err := os.Rename(f.target(), filepath.Join(f.journal(), "attachment")); err != nil {
					t.Fatal(err)
				}
				if phase == "staged" {
					if err := os.Remove(preparedPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			partial := filepath.Join(f.journal(), ".staged-attachment-interrupted")
			if err := os.WriteFile(partial, []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			attachmentApply(t, f)
			if after := attachmentStat(t, f.target()); !published.matchesMoved(&after) {
				t.Fatal("retry did not publish the original staged inode")
			}
			if phase != "staged" && !bytes.Equal(attachmentRead(t, preparedPath), receipt) {
				t.Fatal("retry rewrote the prepared identity receipt")
			}
			if string(attachmentRead(t, partial)) != "partial" || !bytes.Equal(attachmentRead(t, f.source), f.data) {
				t.Fatal("retry discarded history or changed source")
			}
		})
	}
}

func TestLocalAttachmentPreparedCollisionAndMissingInodeRemainHeld(t *testing.T) {
	f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
	attachmentApply(t, f)
	staged := filepath.Join(f.journal(), "attachment")
	if err := os.Rename(f.target(), staged); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.journal(), "complete")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.target(), f.data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyLocalClient(f.root, f.r); err == nil {
		t.Fatal("prepared operation adopted unrelated collision")
	}
	if !bytes.Equal(attachmentRead(t, staged), f.data) || !bytes.Equal(attachmentRead(t, f.target()), f.data) {
		t.Fatal("collision discarded a variant")
	}
	if err := os.Remove(f.target()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, staged+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyLocalClient(f.root, f.r); err == nil {
		t.Fatal("missing prepared inode silently restaged")
	}
	attachmentAbsent(t, f.target())
	if err := os.Rename(staged+".retained", staged); err != nil {
		t.Fatal(err)
	}
	attachmentApply(t, f)
}

func TestLocalAttachmentIntentAndReceiptsCannotBeRebound(t *testing.T) {
	for _, change := range []string{"source", "hash", "size", "missing-source-receipt", "journal-link"} {
		t.Run(change, func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
			// Exercise the request's actual JSON field names without a command shim.
			payload, err := json.Marshal(map[string]any{
				"root_identity": f.r.RootIdentity, "id": f.r.ID, "kind": "import_attachment",
				"path": f.r.Path, "source": f.source, "sha256": f.r.SHA256, "size": f.r.Size,
			})
			if err != nil {
				t.Fatal(err)
			}
			var decoded LocalClientRequest
			if err := json.Unmarshal(payload, &decoded); err != nil || decoded != f.r {
				t.Fatalf("request fields did not round trip: %#v, %v", decoded, err)
			}
			attachmentApply(t, f)
			before := attachmentStat(t, f.target())
			switch change {
			case "source":
				f.r.Source = f.source + ".other"
				if err := os.WriteFile(f.r.Source, f.data, 0600); err != nil {
					t.Fatal(err)
				}
			case "hash":
				f.r.SHA256 = digest([]byte("other"))
			case "size":
				f.r.Size++
			case "missing-source-receipt":
				if err := os.Remove(filepath.Join(f.journal(), "attachment-source.json")); err != nil {
					t.Fatal(err)
				}
			case "journal-link":
				retained := f.journal() + "-retained"
				if err := os.Rename(f.journal(), retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(retained, f.journal()); err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyLocalClient(f.root, f.r); err == nil {
				t.Fatal("changed intent or missing/replaced custody receipt accepted")
			}
			if attachmentStat(t, f.target()) != before {
				t.Fatal("failed replay changed publication")
			}
		})
	}
}

func TestLocalAttachmentSourceReplacementAndAncestorChanges(t *testing.T) {
	for _, phase := range []string{"pinned-file", "pinned-parent", "replay"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
			if phase == "replay" {
				attachmentApply(t, f)
				if err := os.Rename(f.source, f.source+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.source, f.data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := ApplyLocalClient(f.root, f.r); err == nil {
					t.Fatal("same-content replacement source accepted on replay")
				}
				return
			}
			source, err := openLocalAttachmentSource(f.source)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			if phase == "pinned-file" {
				if err := os.Rename(f.source, f.source+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.source, f.data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				dir := filepath.Dir(f.source)
				if err := os.Rename(dir, dir+"-retained"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(dir + "-retained") })
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := os.Open(filepath.Join(f.root, "Attachments"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			if err := stageLocalAttachment(source, journal, f.r); err == nil {
				t.Fatal("source replacement during streaming was accepted")
			}
			attachmentAbsent(t, filepath.Join(f.root, "Attachments", "attachment"))
			attachmentAbsent(t, f.target())
		})
	}
}

func TestLocalAttachmentRejectsUnsafeSourceAndDestination(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *localAttachmentFixture)
	}{
		{"relative-source", func(t *testing.T, f *localAttachmentFixture) { f.r.Source = "private-staging.bin" }},
		{"unclean-source", func(t *testing.T, f *localAttachmentFixture) {
			f.r.Source = filepath.Dir(f.source) + "/../" + filepath.Base(f.source)
		}},
		{"source-link", func(t *testing.T, f *localAttachmentFixture) {
			link := f.source + ".link"
			if err := os.Symlink(f.source, link); err != nil {
				t.Fatal(err)
			}
			f.r.Source = link
		}},
		{"source-ancestor-link", func(t *testing.T, f *localAttachmentFixture) {
			link := filepath.Join(f.root, "source-link")
			if err := os.Symlink(filepath.Dir(f.source), link); err != nil {
				t.Fatal(err)
			}
			f.r.Source = filepath.Join(link, filepath.Base(f.source))
		}},
		{"source-directory", func(t *testing.T, f *localAttachmentFixture) { f.r.Source = filepath.Dir(f.source) }},
		{"source-fifo", func(t *testing.T, f *localAttachmentFixture) {
			fifo := f.source + ".fifo"
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			f.r.Source = fifo
		}},
		{"source-hardlink", func(t *testing.T, f *localAttachmentFixture) {
			if err := os.Link(f.source, f.source+".link"); err != nil {
				t.Fatal(err)
			}
		}},
		{"path-escape", func(t *testing.T, f *localAttachmentFixture) { f.r.Path = "../escape.png" }},
		{"absolute-target", func(t *testing.T, f *localAttachmentFixture) { f.r.Path = f.target() }},
		{"missing-parent", func(t *testing.T, f *localAttachmentFixture) { f.r.Path = "Absent/picture.png" }},
		{"target-link", func(t *testing.T, f *localAttachmentFixture) {
			if err := os.Symlink(f.source, f.target()); err != nil {
				t.Fatal(err)
			}
		}},
		{"target-ancestor-link", func(t *testing.T, f *localAttachmentFixture) {
			link := filepath.Join(f.root, "linked")
			if err := os.Symlink(filepath.Dir(f.source), link); err != nil {
				t.Fatal(err)
			}
			f.r.Path = "linked/picture.png"
		}},
		{"wrong-root-id", func(t *testing.T, f *localAttachmentFixture) { f.r.RootIdentity = "0:0" }},
		{"root-link", func(t *testing.T, f *localAttachmentFixture) {
			link := filepath.Join(filepath.Dir(f.source), "root-link")
			if err := os.Symlink(f.root, link); err != nil {
				t.Fatal(err)
			}
			f.root = link
		}},
		{"replaced-root", func(t *testing.T, f *localAttachmentFixture) {
			retained := filepath.Join(filepath.Dir(f.source), "old-root")
			if err := os.Rename(f.root, retained); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(f.root, "Attachments"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLocalAttachmentFixture(t, []byte{0, 1, 0xff})
			tt.change(t, &f)
			if err := ApplyLocalClient(f.root, f.r); err == nil {
				t.Fatal("unsafe request accepted")
			}
			if !bytes.Equal(attachmentRead(t, f.source), f.data) {
				t.Fatal("rejected request changed original source")
			}
			if tt.name != "target-link" {
				attachmentAbsent(t, filepath.Join(f.root, "Attachments/picture.png"))
			}
		})
	}
}
