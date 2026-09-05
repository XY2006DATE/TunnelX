package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Authenticator Token认证器
type Authenticator struct {
	token     string
	tokenHash string
	mu        sync.RWMutex
}

// NewAuthenticator 创建认证器
func NewAuthenticator(token string) *Authenticator {
	hash := sha256.Sum256([]byte(token))
	return &Authenticator{
		token:     token,
		tokenHash: hex.EncodeToString(hash[:]),
	}
}

// Verify 验证Token
func (a *Authenticator) Verify(token string) bool {
	if a.token == "" {
		return true // 未配置token，允许所有连接
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	return token == a.token
}

// GetToken 获取Token
func (a *Authenticator) GetToken() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token
}

// UpdateToken 更新Token
func (a *Authenticator) UpdateToken(newToken string) {
	hash := sha256.Sum256([]byte(newToken))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = newToken
	a.tokenHash = hex.EncodeToString(hash[:])
}

// Session 会话管理
type Session struct {
	ClientID  string
	Token     string
	CreatedAt time.Time
	LastSeen  time.Time
}

// SessionManager 会话管理器
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// NewSessionManager 创建会话管理器
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// CreateSession 创建会话
func (sm *SessionManager) CreateSession(clientID, token string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session := &Session{
		ClientID:  clientID,
		Token:     token,
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}
	sm.sessions[clientID] = session
	return session
}

// GetSession 获取会话
func (sm *SessionManager) GetSession(clientID string) (*Session, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[clientID]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	return session, nil
}

// UpdateLastSeen 更新最后活跃时间
func (sm *SessionManager) UpdateLastSeen(clientID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, ok := sm.sessions[clientID]; ok {
		session.LastSeen = time.Now()
	}
}

// DeleteSession 删除会话
func (sm *SessionManager) DeleteSession(clientID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, clientID)
}

// CleanupExpiredSessions 清理过期会话
func (sm *SessionManager) CleanupExpiredSessions(timeout time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for clientID, session := range sm.sessions {
		if now.Sub(session.LastSeen) > timeout {
			delete(sm.sessions, clientID)
		}
	}
}
