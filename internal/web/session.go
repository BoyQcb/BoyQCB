package web

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SessionStore 内存会话（无第三方依赖，进程级）
type SessionStore struct {
	mu     sync.Mutex
	m      map[string]time.Time
	maxAge time.Duration
}

// NewSessionStore 构造会话存储
func NewSessionStore() *SessionStore {
	return &SessionStore{m: map[string]time.Time{}, maxAge: 24 * time.Hour}
}

// Issue 签发新会话令牌
func (s *SessionStore) Issue() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = time.Now().Add(s.maxAge)
	s.mu.Unlock()
	return tok, nil
}

// Valid 校验令牌是否有效（过期自动清除）
func (s *SessionStore) Valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	return true
}

// Revoke 撤销令牌
func (s *SessionStore) Revoke(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}
