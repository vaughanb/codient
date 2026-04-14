package codeindex

import (
	"encoding/gob"
	"os"
	"path/filepath"
)

const indexDir = ".codient/index"
const indexFile = "embeddings.gob"

type storedIndex struct {
	Model   string
	Entries []Entry
}

func indexPath(workspace string) string {
	return filepath.Join(workspace, indexDir, indexFile)
}

// loadIndex reads the persisted index. Returns nil entries (not an error) when
// the file is missing or the stored model differs from the current one.
func loadIndex(workspace, model string) ([]Entry, error) {
	path := indexPath(workspace)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var stored storedIndex
	if err := gob.NewDecoder(f).Decode(&stored); err != nil {
		return nil, nil
	}
	if stored.Model != model {
		return nil, nil
	}
	return stored.Entries, nil
}

// saveIndex writes the index atomically to disk.
func saveIndex(workspace, model string, entries []Entry) error {
	dir := filepath.Join(workspace, indexDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := indexPath(workspace)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	stored := storedIndex{Model: model, Entries: entries}
	if err := gob.NewEncoder(f).Encode(&stored); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
