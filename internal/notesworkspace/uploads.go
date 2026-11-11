package notesworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
)

const UploadChunkBytes = 1 << 20
const MaxUploadBytes int64 = 2 << 30
const uploadBudget int64 = 16 << 30

type UploadIntent struct {
	BoxID     string `json:"box_id,omitempty"`
	ID        string `json:"id"`
	Root      string `json:"root"`
	Path      string `json:"path"`
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	Collision string `json:"collision"`
}
type Upload struct {
	Intent      UploadIntent            `json:"intent"`
	Path        string                  `json:"path"`
	Collection  string                  `json:"collection"`
	Generation  string                  `json:"generation"`
	Parent      ImportParent            `json:"parent"`
	Offset      int64                   `json:"offset"`
	State       string                  `json:"state"`
	Reason      string                  `json:"reason,omitempty"`
	Blob        *objectstore.StoredBlob `json:"-"`
	Receipt     *Operation              `json:"receipt,omitempty"`
	StagingHeld bool                    `json:"staging_held,omitempty"`
}
type uploadRecord struct {
	Upload
	RetainedBlob *objectstore.StoredBlob `json:"retained_blob,omitempty"`
}
type Uploads struct {
	DB        *sql.DB
	Directory string
	Blobs     objectstore.Store
}

func ValidateUploadIntent(v UploadIntent) error {
	if !strings.HasPrefix(v.ID, "desktop_upload_") || len(v.ID) > 100 || strings.TrimPrefix(v.ID, "desktop_upload_") == "" || strings.Trim(strings.TrimPrefix(v.ID, "desktop_upload_"), "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" || !boxregistry.IsBoxRef(v.Root) || validateDirectoryPath(v.Path) != nil || len(v.Path) > 4096 || v.Size < 0 || v.Size > MaxUploadBytes || len(v.Hash) != 71 || !strings.HasPrefix(v.Hash, "sha256:") || strings.Trim(strings.TrimPrefix(v.Hash, "sha256:"), "0123456789abcdef") != "" || (v.Collision != "skip" && v.Collision != "keep_both") {
		return ErrUnsafePath
	}
	if v.BoxID != "" && (!boxregistry.IsBoxRef("box:"+v.BoxID) || v.Root != "box" && v.Root != "box:"+v.BoxID) {
		return ErrUnsafePath
	}
	return nil
}

func (s Uploads) lock(ctx context.Context, id string, fn func(*sql.Conn) error) (err error) {
	conn, e := s.DB.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	key := "desktop-upload:" + id
	if _, e = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); e != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var ok bool
		e := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&ok)
		if e != nil || !ok {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("upload lock release failed"))
		}
	}()
	return fn(conn)
}
func uploadRead(ctx context.Context, q sqlQuery, id string) (v Upload, err error) {
	var raw []byte
	err = q.QueryRowContext(ctx, `SELECT record FROM notes_workspace.desktop_imports WHERE import_id=$1`, id).Scan(&raw)
	var record uploadRecord
	if err == nil {
		err = json.Unmarshal(raw, &record)
		v = record.Upload
		v.Blob = record.RetainedBlob
	}
	return
}
func uploadSave(ctx context.Context, q sqlQuery, v Upload) error {
	raw, e := json.Marshal(uploadRecord{Upload: v, RetainedBlob: v.Blob})
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO notes_workspace.desktop_imports(import_id,record) VALUES($1,$2) ON CONFLICT(import_id) DO UPDATE SET record=EXCLUDED.record,updated_at=now()`, v.Intent.ID, raw)
	return e
}
func (s Uploads) Get(ctx context.Context, id string) (Upload, error) {
	return uploadRead(ctx, s.DB, id)
}

func (s Uploads) Begin(ctx context.Context, owner Service, collection string, intent UploadIntent) (out Upload, err error) {
	if err = ValidateUploadIntent(intent); err != nil {
		return
	}
	// Serialize admission only; payload transfer never holds the filesystem fence.
	err = s.lock(ctx, "admission", func(q *sql.Conn) error {
		old, e := uploadRead(ctx, q, intent.ID)
		if e == nil {
			if old.Intent != intent {
				return ErrIntentMismatch
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var reserved int64
		var active int
		if e = q.QueryRowContext(ctx, `SELECT COALESCE(sum((record->'intent'->>'size')::bigint),0),count(*) FROM notes_workspace.desktop_imports WHERE record->>'state' IN ('receiving','ready') OR record->>'staging_held'='true'`).Scan(&reserved, &active); e != nil {
			return e
		}
		if active >= 64 || reserved+intent.Size > uploadBudget {
			return fmt.Errorf("upload staging capacity reached; finish or cancel an existing upload")
		}
		out = Upload{Intent: intent, Collection: collection, Path: intent.Path, State: "receiving"}
		for attempt := 0; attempt < 100; attempt++ {
			out.Generation, out.Parent, e = owner.ImportDestination(ctx, collection, out.Path)
			if e == nil {
				break
			}
			if !errors.Is(e, os.ErrExist) {
				return e
			}
			if intent.Collision == "skip" {
				out.State, out.Reason = "skipped", "destination exists; nothing replaced"
				break
			}
			ext := path.Ext(intent.Path)
			stem := strings.TrimSuffix(path.Base(intent.Path), ext)
			out.Path = path.Join(path.Dir(intent.Path), fmt.Sprintf("%s (%d)%s", stem, attempt+2, ext))
		}
		if e != nil && out.State != "skipped" {
			return e
		}
		if e = uploadSave(ctx, q, out); e != nil {
			return e
		}
		return nil
	})
	return
}

func (s Uploads) staging(id string) (*os.Root, error) {
	if !strings.HasPrefix(id, "desktop_upload_") || strings.ContainsAny(id, "./\\\x00") {
		return nil, ErrUnsafePath
	}
	if e := os.MkdirAll(s.Directory, 0700); e != nil {
		return nil, e
	}
	root, e := os.OpenRoot(s.Directory)
	if e != nil {
		return nil, e
	}
	if e = root.Mkdir(id, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		root.Close()
		return nil, e
	}
	child, e := root.OpenRoot(id)
	if e == nil {
		parent, openErr := os.Open(filepath.Dir(s.Directory))
		if openErr == nil {
			openErr = parent.Sync()
			parent.Close()
		}
		if openErr == nil {
			openErr = syncUploadRoot(root)
		}
		if openErr != nil {
			child.Close()
			root.Close()
			return nil, openErr
		}
	}
	root.Close()
	return child, e
}
func syncUploadRoot(root *os.Root) error {
	file, e := root.Open(".")
	if e != nil {
		return e
	}
	defer file.Close()
	return file.Sync()
}
func (s Uploads) Chunk(ctx context.Context, id string, offset int64, payload []byte, checksum string) (out Upload, err error) {
	h := sha256.Sum256(payload)
	if len(payload) == 0 || len(payload) > UploadChunkBytes || offset < 0 || "sha256:"+hex.EncodeToString(h[:]) != checksum {
		return out, ErrIntentMismatch
	}
	err = s.lock(ctx, id, func(q *sql.Conn) error {
		v, e := uploadRead(ctx, q, id)
		if e != nil {
			return e
		}
		out = v
		if v.State != "receiving" {
			return fmt.Errorf("upload is %s", v.State)
		}
		if offset > v.Offset || offset+int64(len(payload)) > v.Intent.Size {
			return ErrIntentMismatch
		}
		root, e := s.staging(id)
		if e != nil {
			return e
		}
		defer root.Close()
		file, e := root.OpenFile("payload", os.O_RDWR|os.O_CREATE, 0600)
		if e != nil {
			return e
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() {
			return ErrUnsafePath
		}
		if offset < v.Offset {
			if offset+int64(len(payload)) > v.Offset {
				return ErrIntentMismatch
			}
			old := make([]byte, len(payload))
			if _, e = file.ReadAt(old, offset); e != nil {
				return e
			}
			if !bytes.Equal(old, payload) {
				return ErrIntentMismatch
			}
			return nil
		}
		if info.Size() < v.Offset {
			return fmt.Errorf("upload staging bytes missing; retained progress is not a complete file")
		}
		// Bytes written before a crashed checkpoint are private and unacknowledged.
		if e = file.Truncate(v.Offset); e != nil {
			return e
		}
		if _, e = file.WriteAt(payload, offset); e != nil {
			return e
		}
		if e = file.Sync(); e != nil {
			return e
		}
		if e = syncUploadRoot(root); e != nil {
			return e
		}
		v.Offset += int64(len(payload))
		if e = uploadSave(ctx, q, v); e != nil {
			return e
		}
		out = v
		return nil
	})
	return
}
func (s Uploads) Cancel(ctx context.Context, id string) (out Upload, err error) {
	err = s.lock(ctx, id, func(q *sql.Conn) error {
		v, e := uploadRead(ctx, q, id)
		if e != nil {
			return e
		}
		if done, e := s.recoverReceipt(ctx, q, &v); e != nil {
			return e
		} else if done {
			out = v
			return nil
		}
		if v.State == "ready" {
			if op, e := (SQLStore{DB: s.DB, query: q}).Operation(ctx, id); e == nil {
				if op.State != Conflict {
					return fmt.Errorf("source publication already started; resume to recover its exact receipt")
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if v.State == "receiving" || v.State == "ready" || v.State == "cancelled" && v.StagingHeld {
			v.State, v.Reason, v.StagingHeld = "cancelled", "source original preserved", true
			if e = uploadSave(ctx, q, v); e != nil {
				return e
			}
			if e = s.releaseSpool(ctx, q, &v); e != nil {
				return e
			}
		}
		out = v
		return nil
	})
	return
}
func (s Uploads) releaseSpool(ctx context.Context, q sqlQuery, v *Upload) error {
	if !v.StagingHeld {
		return nil
	}
	root, e := s.staging(v.Intent.ID)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = root.Remove("payload"); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = syncUploadRoot(root); e != nil {
		return e
	}
	v.StagingHeld = false
	return uploadSave(ctx, q, *v)
}
func (s Uploads) recoverReceipt(ctx context.Context, q sqlQuery, v *Upload) (bool, error) {
	op, e := (SQLStore{DB: s.DB, query: q}).Operation(ctx, v.Intent.ID)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if op.Request.Kind != KindImport || op.File.CollectionID != v.Collection || op.File.RelativePath != v.Path || op.Request.Generation != v.Generation || op.Request.ImportHash != v.Intent.Hash || op.Request.ImportSize != v.Intent.Size || op.ImportParent == nil || *op.ImportParent != (inodeIdentity{v.Parent.Device, v.Parent.Inode}) {
		return false, ErrIntentMismatch
	}
	if op.State != Accepted {
		return false, nil
	}
	if v.State != "accepted" {
		v.State, v.Reason, v.Receipt, v.StagingHeld = "accepted", "source bytes verified; indexing and backup are separate", &op, true
		if e = uploadSave(ctx, q, *v); e != nil {
			return false, e
		}
	}
	return true, s.releaseSpool(ctx, q, v)
}

// Replay is independent of the live parent. Publication may have committed
// before the upload receipt checkpoint, followed by a legitimate folder move.
func (s Uploads) Replay(ctx context.Context, id string) (out Upload, done bool, err error) {
	err = s.lock(ctx, id, func(q *sql.Conn) error {
		var e error
		out, e = uploadRead(ctx, q, id)
		if e != nil {
			return e
		}
		if out.State == "skipped" {
			done = true
			return nil
		}
		done, e = s.recoverReceipt(ctx, q, &out)
		return e
	})
	return
}
func (s Uploads) Complete(ctx context.Context, id string, owner Service) (out Upload, err error) {
	err = s.lock(ctx, id, func(q *sql.Conn) error {
		v, e := uploadRead(ctx, q, id)
		if e != nil {
			return e
		}
		out = v
		if done, e := s.recoverReceipt(ctx, q, &v); e != nil {
			return e
		} else if done {
			out = v
			return nil
		}
		if v.State == "skipped" {
			return nil
		}
		if v.State != "receiving" && v.State != "ready" {
			return fmt.Errorf("upload is %s", v.State)
		}
		if v.Offset != v.Intent.Size {
			return fmt.Errorf("upload payload incomplete")
		}
		if v.Blob == nil {
			root, e := s.staging(id)
			if e != nil {
				return e
			}
			defer root.Close()
			file, e := root.OpenFile("payload", os.O_RDWR|os.O_CREATE, 0600)
			if e != nil {
				return e
			}
			info, e := file.Stat()
			if e != nil || !info.Mode().IsRegular() || info.Size() != v.Intent.Size {
				file.Close()
				return ErrIntentMismatch
			}
			file.Close()
			blob, e := s.Blobs.PutFileVerified(ctx, filepath.Join(s.Directory, id, "payload"), v.Intent.Hash, v.Intent.Size)
			if e != nil {
				return e
			}
			if blob.HashURI != v.Intent.Hash || blob.SizeBytes != v.Intent.Size {
				return ErrIntentMismatch
			}
			// Retain and reference once, without creating an extraction/embedding job.
			var blobID string
			tx, e := q.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			e = tx.QueryRowContext(ctx, `INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,verified_at,status,metadata) VALUES($1,$2,$3,$4,$5,$6,now(),'verified','{"owner":"workspace_import"}'::jsonb) ON CONFLICT(hash_algorithm,hash_hex) DO UPDATE SET verified_at=files.blobs.verified_at RETURNING blob_id`, ids.NewBlobID(), blob.HashAlgorithm, blob.HashHex, blob.HashURI, blob.SizeBytes, blob.StoragePath).Scan(&blobID)
			if e != nil {
				return e
			}
			v.Blob, v.State, v.StagingHeld = &blob, "ready", true
			if e = uploadSave(ctx, tx, v); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE notes_workspace.desktop_imports SET blob_id=$2 WHERE import_id=$1`, id, blobID); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
		}
		owner.Blobs = s.Blobs
		receipt, e := owner.CreateImport(ctx, v.Collection, v.Path, id, v.Generation, v.Parent, *v.Blob)
		if e != nil {
			if receipt.State == Conflict {
				v.Reason = receipt.Reason
				_ = uploadSave(ctx, q, v)
			}
			return e
		}
		if receipt.State != Accepted {
			return fmt.Errorf("upload source not accepted: %s", receipt.Reason)
		}
		v.State, v.Reason, v.Receipt, v.StagingHeld = "accepted", "source bytes verified; indexing and backup are separate", &receipt, true
		if e = uploadSave(ctx, q, v); e != nil {
			return e
		}
		if e = s.releaseSpool(ctx, q, &v); e != nil {
			return e
		}
		out = v
		return nil
	})
	return
}
