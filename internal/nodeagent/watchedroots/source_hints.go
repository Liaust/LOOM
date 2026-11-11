package watchedroots

import (
	"crypto/rand"
	"errors"
	"path/filepath"
	"time"
)

// SourceHints survives the scanner's acknowledgement of dirty-hints.json.
// Consumers use it only for invalidation; canonical source reads remain required.
type SourceHints struct {
	RootPath   string               `json:"root_path"`
	ConfigHash string               `json:"config_hash"`
	Hints      map[string]DirtyHint `json:"hints"`
}

func (s Store) SourceHintsPath(rootKey string) string {
	return filepath.Join(s.RootDir(rootKey), "source-hints.json")
}

func (s Store) LoadSourceHints(rootKey string) (SourceHints, error) {
	var hints SourceHints
	err := readJSONFile(s.SourceHintsPath(rootKey), &hints)
	return hints, err
}

func (s Store) publishSourceHints(root ValidatedRoot, additions []DirtyHint) error {
	release, err := s.dirtyHintsLock(root.Config.RootKey)
	if err != nil {
		return err
	}
	defer release()
	feed, err := s.LoadSourceHints(root.Config.RootKey)
	if err != nil && !IsNotExist(err) {
		return err
	}
	if feed.RootPath != root.RootPath || feed.ConfigHash != root.ConfigHash || feed.Hints == nil {
		feed = SourceHints{RootPath: root.RootPath, ConfigHash: root.ConfigHash, Hints: map[string]DirtyHint{}}
	}
	for _, hint := range additions {
		hint.RootKey, hint.RelativePath = root.Config.RootKey, normalizeDirtyPath(hint.RelativePath)
		hint.Generation, hint.LastSeenAt = rand.Text(), time.Now().UTC()
		feed.Hints[dirtyHintKey(hint.RelativePath)] = hint
	}
	if len(feed.Hints) > 4096 {
		// Overflow requests bounded reconciliation, never deletion.
		for _, hint := range additions {
			hint.RelativePath, hint.HintKind = "", DirtyHintRescanRequired
			feed.Hints = map[string]DirtyHint{"__rescan__": hint}
			break
		}
	}
	if feed.RootPath == "" || len(feed.Hints) > 4096 {
		return errors.New("source hint scope unavailable")
	}
	return writeJSONFile(s.SourceHintsPath(root.Config.RootKey), feed, 0o600)
}
