package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/staltz/fabrica/internal/paths"
)

// fileName is the credentials file, in Fabrica's config folder.
const fileName = "credentials.json"

// fileStore keeps credentials in a JSON file that only you can read, for
// machines without a keychain Fabrica can use: servers, containers, WSL.
// It's as safe as your SSH keys: anyone who can read your files can read
// it, and nobody else can.
type fileStore struct {
	path string
}

func defaultFile() (*fileStore, error) {
	dir, err := paths.Config()
	if err != nil {
		return nil, err
	}
	return &fileStore{path: filepath.Join(dir, fileName)}, nil
}

func (f *fileStore) Name() string {
	return tildePath(f.path) + ", a file only you can read"
}

func (f *fileStore) id() string { return "file:" + f.path }

func (f *fileStore) Get(key string) (string, bool, error) {
	all, err := f.read()
	if err != nil {
		return "", false, err
	}
	v, ok := all[key]
	return v, ok, nil
}

func (f *fileStore) Set(key, value string) error {
	all, err := f.read()
	if err != nil {
		return err
	}
	all[key] = value
	return f.write(all)
}

func (f *fileStore) Delete(key string) (bool, error) {
	all, err := f.read()
	if err != nil {
		return false, err
	}
	if _, ok := all[key]; !ok {
		return false, nil
	}
	delete(all, key)
	return true, f.write(all)
}

// read reads the whole file. A missing file is an empty one. A file that
// others can read is an error, the way SSH refuses a private key anyone
// can read: its secrets may have leaked, and Fabrica won't pretend
// otherwise.
func (f *fileStore) read() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(f.path)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s can be read by other users, so Fabrica won't use it; if nobody else could have read it, run `chmod 600 %s`, otherwise replace the credentials in it", f.path, f.path)
		}
	}
	all := map[string]string{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return all, nil
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s isn't the JSON Fabrica writes: %v", f.path, err)
	}
	return all, nil
}

// write replaces the file atomically, readable only by you, in a folder
// only you can open.
func (f *fileStore) write(all map[string]string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(all) == 0 {
		err := os.Remove(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ") // sorted by key
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// tildePath writes a path in your home folder as ~/….
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return p
}
