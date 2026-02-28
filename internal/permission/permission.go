package permission

import (
	"context"
	"sync"
)

// Response represents the user's permission decision.
type Response int

const (
	Allow Response = iota
	Deny
	AllowForSession
)

// Request represents a tool asking for user permission.
type Request struct {
	ToolName    string
	Description string
	Input       string
	ResponseCh  chan Response
}

// Service manages tool execution permissions.
type Service struct {
	mu             sync.RWMutex
	sessionAllowed map[string]bool // tools allowed for the entire session
	requestCh      chan Request    // channel to send permission requests to the TUI
}

// NewService creates a new permission service.
func NewService() *Service {
	return &Service{
		sessionAllowed: make(map[string]bool),
		requestCh:      make(chan Request, 1),
	}
}

// RequestCh returns the channel that receives permission requests (for the TUI to listen on).
func (s *Service) RequestCh() <-chan Request {
	return s.requestCh
}

// Check checks if a tool is allowed to execute. If the tool has been allowed
// for the session, returns Allow immediately. Otherwise, sends a request to
// the TUI and blocks until the user responds or the context is cancelled.
func (s *Service) Check(ctx context.Context, toolName string, input string) Response {
	s.mu.RLock()
	if s.sessionAllowed[toolName] {
		s.mu.RUnlock()
		return Allow
	}
	s.mu.RUnlock()

	// Send a permission request and wait for the response
	respCh := make(chan Response, 1)
	req := Request{
		ToolName:    toolName,
		Description: toolName,
		Input:       input,
		ResponseCh:  respCh,
	}

	// Try to send the request, but respect cancellation
	select {
	case s.requestCh <- req:
	case <-ctx.Done():
		return Deny
	}

	// Wait for the user's response, but respect cancellation
	select {
	case resp := <-respCh:
		if resp == AllowForSession {
			s.mu.Lock()
			s.sessionAllowed[toolName] = true
			s.mu.Unlock()
			return Allow
		}
		return resp
	case <-ctx.Done():
		return Deny
	}
}

// Reset clears all session-level permissions.
func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionAllowed = make(map[string]bool)
}

// IsAllowed checks if a tool is already allowed without prompting.
func (s *Service) IsAllowed(toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionAllowed[toolName]
}
