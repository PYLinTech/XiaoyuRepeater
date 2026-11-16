package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"xiaoyurepeater/internal/password"
)

const stateFileName = "state.json"

type APIKey struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	Hash       string    `json:"hash"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

type state struct {
	AdminPasswordHash string   `json:"admin_password_hash,omitempty"`
	APIKeys           []APIKey `json:"api_keys"`
}

// Store keeps the small amount of durable state in one JSON file.
// Rooms and sessions are intentionally in-memory.
type Store struct {
	mu    sync.Mutex
	dir   string
	path  string
	state state
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &Store{dir: dir, path: filepath.Join(dir, stateFileName)}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.state); err != nil {
			return nil, fmt.Errorf("解析状态文件失败: %w", err)
		}
	}
	return s, nil
}

func (s *Store) HasPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.AdminPasswordHash != ""
}

func (s *Store) SetPassword(pw string) error {
	hash, err := password.HashPassword(pw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.AdminPasswordHash = hash
	return s.saveLocked()
}

func (s *Store) VerifyPassword(pw string) bool {
	s.mu.Lock()
	hash := s.state.AdminPasswordHash
	s.mu.Unlock()
	if hash == "" {
		return false
	}
	return password.VerifyPassword(hash, pw)
}

func (s *Store) ListKeys() []APIKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]APIKey, len(s.state.APIKeys))
	copy(keys, s.state.APIKeys)
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].CreatedAt.After(keys[j].CreatedAt)
	})
	for i := range keys {
		keys[i].Hash = ""
	}
	return keys
}

func (s *Store) GetKey(id string) (APIKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.state.APIKeys {
		if k.ID == id {
			k.Hash = ""
			return k, true
		}
	}
	return APIKey{}, false
}

func (s *Store) CreateKey(name string) (APIKey, string, error) {
	secret, err := randomToken(24)
	if err != nil {
		return APIKey{}, "", err
	}
	secret = "xr_" + secret
	key := APIKey{
		ID:        "k_" + mustToken(8),
		Name:      name,
		Prefix:    secret[:10],
		Hash:      hashSecret(secret),
		CreatedAt: time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.APIKeys = append(s.state.APIKeys, key)
	if err := s.saveLocked(); err != nil {
		s.state.APIKeys = s.state.APIKeys[:len(s.state.APIKeys)-1]
		return APIKey{}, "", err
	}
	key.Hash = ""
	return key, secret, nil
}

func (s *Store) LookupKey(secret string) (APIKey, bool) {
	h := hashSecret(strings.TrimSpace(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.state.APIKeys {
		if k.Hash == h {
			k.Hash = ""
			return k, true
		}
	}
	return APIKey{}, false
}

func (s *Store) DeleteKey(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.state.APIKeys {
		if k.ID == id {
			removed := s.state.APIKeys[i]
			s.state.APIKeys = append(s.state.APIKeys[:i], s.state.APIKeys[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.state.APIKeys = append(s.state.APIKeys[:i], append([]APIKey{removed}, s.state.APIKeys[i:]...)...)
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) TouchKey(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.APIKeys {
		if s.state.APIKeys[i].ID == id {
			s.state.APIKeys[i].LastUsedAt = at.UTC()
			return s.saveLocked()
		}
	}
	return nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func mustToken(n int) string {
	t, err := randomToken(n)
	if err != nil {
		panic(err)
	}
	return t
}
