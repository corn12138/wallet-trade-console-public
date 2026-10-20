// Package adminclient is the controlled operator client for the versioned
// campaign administrator API. It owns local credential custody, durable command
// intents and response classification; the server remains the only authority
// for authorization, CAS, hashes and command outcomes.
package adminclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Home is a private operator directory. Credentials and intents never leave it,
// so it must be a real directory owned by this user with no group/other access.
type Home struct{ Dir string }

var ErrCredentialMissing = errors.New("no administrator session; run login prepare/complete")
var ErrCredentialExpired = errors.New("administrator session expired; log in again")

func OpenHome(dir string) (Home, error) {
	if dir == "" {
		return Home{}, errors.New("home directory required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "intents"), 0o700); err != nil {
		return Home{}, err
	}
	for _, path := range []string{dir, filepath.Join(dir, "intents")} {
		if err := requirePrivate(path, true); err != nil {
			return Home{}, err
		}
	}
	return Home{Dir: dir}, nil
}

// requirePrivate rejects symlinks and any group/other permission bit, mirroring
// ssh's refusal to use a readable key: a widened file is treated as disclosed.
func requirePrivate(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != dir || (!dir && !info.Mode().IsRegular()) {
		return fmt.Errorf("%s must be a regular private %s, not a link", filepath.Base(path), map[bool]string{true: "directory", false: "file"}[dir])
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions %v are broader than owner-only", filepath.Base(path), info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user", filepath.Base(path))
	}
	return nil
}

// writePrivate replaces a file atomically with owner-only permissions. The data
// is fsynced before rename so a crash leaves either the old or the new record,
// never a truncated intent whose payload no longer matches its digest.
func writePrivate(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(append(raw, '\n')); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readPrivate(path string, target any) error {
	if err := requirePrivate(path, false); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// Credential holds a bearer token, so it is only ever written to the private
// home and never printed, logged, placed in argv or copied into an intent.
type Credential struct {
	API              string `json:"api"`
	Address          string `json:"address"`
	ChainID          int    `json:"chainId"`
	Token            string `json:"token"`
	SessionExpiresAt string `json:"sessionExpiresAt"`
}

func (h Home) credentialPath() string { return filepath.Join(h.Dir, "credential.json") }

func (h Home) SaveCredential(value Credential) error { return writePrivate(h.credentialPath(), value) }

func (h Home) LoadCredential(now time.Time) (Credential, error) {
	var value Credential
	if err := readPrivate(h.credentialPath(), &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return value, ErrCredentialMissing
		}
		return value, err
	}
	expires, err := time.Parse(time.RFC3339Nano, value.SessionExpiresAt)
	if err != nil || value.Token == "" || !walletPattern.MatchString(value.Address) {
		return Credential{}, errors.New("stored administrator session is malformed; log in again")
	}
	if !now.Before(expires) {
		return Credential{}, ErrCredentialExpired
	}
	return value, nil
}

// RemoveCredential deletes the local copy only. Logout first ends the session
// on the server; until that is confirmed, a copied token stays valid until
// its expiry and a leaked file must be treated as a live credential.
func (h Home) RemoveCredential() error {
	err := os.Remove(h.credentialPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
