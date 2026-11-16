package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xiaoyurepeater/internal/password"
	"xiaoyurepeater/internal/store"
)

var (
	ErrSetupRequired  = errors.New("尚未设置管理密码")
	ErrAlreadySetup   = errors.New("管理密码已设置")
	ErrInvalidLogin   = errors.New("密码不正确")
	ErrEnvManaged     = errors.New("密码由 XR_ADMIN_PASSWORD 环境变量管理，无法在控制台修改")
	ErrWeakPassword   = errors.New("密码至少需要 8 个字符")
	ErrSessionMissing = errors.New("未登录或登录已过期")
)

const (
	loginFailureWindow = 5 * time.Minute
	loginFailureLimit  = 8
)

type Service struct {
	store  *store.Store
	envHash string
	ttl    time.Duration

	mu       sync.Mutex
	sessions map[string]time.Time
	failures map[string][]time.Time
}

func NewService(st *store.Store, envPassword string, ttl time.Duration) (*Service, error) {
	s := &Service{
		store:    st,
		ttl:      ttl,
		sessions: make(map[string]time.Time),
		failures: make(map[string][]time.Time),
	}
	if envPassword != "" {
		hash, err := password.HashPassword(envPassword)
		if err != nil {
			return nil, fmt.Errorf("处理 XR_ADMIN_PASSWORD 失败: %w", err)
		}
		s.envHash = hash
	}
	return s, nil
}

func (s *Service) EnvManaged() bool { return s.envHash != "" }

func (s *Service) SetupRequired() bool {
	return !s.EnvManaged() && !s.store.HasPassword()
}

func (s *Service) Setup(password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	if s.EnvManaged() {
		return ErrEnvManaged
	}
	if !s.store.HasPassword() {
		return s.store.SetPassword(password)
	}
	return ErrAlreadySetup
}

func (s *Service) Verify(pw string) bool {
	if s.envHash != "" {
		return password.VerifyPassword(s.envHash, pw)
	}
	return s.store.VerifyPassword(pw)
}

func (s *Service) ChangePassword(current, next string) error {
	if s.EnvManaged() {
		return ErrEnvManaged
	}
	if !s.Verify(current) {
		return ErrInvalidLogin
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	return s.store.SetPassword(next)
}

func (s *Service) CreateSession() (string, time.Time) {
	token := randomSessionToken()
	expires := time.Now().Add(s.ttl)
	s.mu.Lock()
	s.sessions[token] = expires
	s.pruneLocked(time.Now())
	s.mu.Unlock()
	return token, expires
}

func (s *Service) ValidSession(token string) bool {
	if token == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[token]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *Service) DestroySession(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *Service) DestroyOtherSessions(keep string) {
	s.mu.Lock()
	for token := range s.sessions {
		if token != keep {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
}

func (s *Service) AllowLogin(ip string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.recentFailuresLocked(ip, now)
	s.failures[ip] = recent
	return len(recent) < loginFailureLimit
}

func (s *Service) RecordLoginFailure(ip string) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[ip] = append(s.recentFailuresLocked(ip, now), now)
}

func (s *Service) ClearLoginFailures(ip string) {
	s.mu.Lock()
	delete(s.failures, ip)
	s.mu.Unlock()
}

func (s *Service) recentFailuresLocked(ip string, now time.Time) []time.Time {
	cutoff := now.Add(-loginFailureWindow)
	existing := s.failures[ip]
	kept := existing[:0]
	for _, t := range existing {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

func (s *Service) pruneLocked(now time.Time) {
	for token, expires := range s.sessions {
		if now.After(expires) {
			delete(s.sessions, token)
		}
	}
}

func validatePassword(password string) error {
	if strings.TrimSpace(password) == "" || len(password) < 8 {
		return ErrWeakPassword
	}
	if len(password) > 200 {
		return errors.New("密码过长")
	}
	return nil
}

func randomSessionToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func SessionCookieName() string { return "xr_admin" }
