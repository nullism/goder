package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/webgovernor/goder/internal/db"
	"github.com/webgovernor/goder/internal/message"
)

// Service manages conversation sessions.
type Service struct {
	db        *db.DB
	currentID string
}

// NewService creates a new session service.
func NewService(database *db.DB) *Service {
	return &Service{db: database}
}

// Create starts a new session and makes it current.
func (s *Service) Create(title string) (*db.Session, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("ses_%s_%s",
		time.Now().Format("20060102150405"),
		hex.EncodeToString(b),
	)
	session, err := s.db.CreateSession(id, title)
	if err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}
	s.currentID = id
	return session, nil
}

// Switch changes the current session.
func (s *Service) Switch(id string) (*db.Session, error) {
	session, err := s.db.GetSession(id)
	if err != nil {
		return nil, fmt.Errorf("switching to session %s: %w", id, err)
	}
	s.currentID = id
	return session, nil
}

// Current returns the current session, creating one if none exists.
func (s *Service) Current() (*db.Session, error) {
	if s.currentID == "" {
		return s.Create("New Session")
	}
	return s.db.GetSession(s.currentID)
}

// CurrentID returns the current session ID.
func (s *Service) CurrentID() string {
	return s.currentID
}

// List returns all sessions.
func (s *Service) List() ([]*db.Session, error) {
	return s.db.ListSessions()
}

// Delete removes a session. If it's the current session, clears the current ID.
func (s *Service) Delete(id string) error {
	if err := s.db.DeleteSession(id); err != nil {
		return fmt.Errorf("deleting session %s: %w", id, err)
	}
	if s.currentID == id {
		s.currentID = ""
	}
	return nil
}

// AddMessage adds a message to the current session.
func (s *Service) AddMessage(msg message.Message) error {
	return s.db.AddMessage(msg)
}

// GetMessages returns all messages for the current session.
func (s *Service) GetMessages() ([]message.Message, error) {
	if s.currentID == "" {
		return nil, nil
	}
	return s.db.GetMessages(s.currentID)
}

// GetMessageCount returns the message count for the current session.
func (s *Service) GetMessageCount() (int, error) {
	if s.currentID == "" {
		return 0, nil
	}
	return s.db.GetMessageCount(s.currentID)
}

// UpdateTitle updates the title of the current session.
func (s *Service) UpdateTitle(title string) error {
	if s.currentID == "" {
		return fmt.Errorf("no current session")
	}
	return s.db.UpdateSessionTitle(s.currentID, title)
}
