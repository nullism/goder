package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool defines the interface that all tools must implement.
type Tool interface {
	// Name returns the tool's unique identifier.
	Name() string

	// Description returns a human-readable description of what the tool does.
	Description() string

	// Parameters returns the JSON Schema for the tool's input parameters.
	Parameters() json.RawMessage

	// RequiresPermission returns true if the tool needs user approval before execution.
	RequiresPermission() bool

	// Execute runs the tool with the given JSON input and returns the output.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// ToolDef is a convenience struct for building JSON Schema tool parameter definitions.
type ToolDef struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// Property defines a single parameter in a JSON Schema.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
}

// Registry holds all registered tools and provides lookup.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string // preserve insertion order
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// All returns all registered tools in insertion order.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, r.tools[name])
	}
	return result
}

// Execute looks up and executes a tool by name.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, input)
}

// DefaultRegistry creates a registry with all built-in tools pre-registered.
func DefaultRegistry(workDir string) *Registry {
	r := NewRegistry()

	// Read-only tools
	r.Register(NewGlobTool(workDir))
	r.Register(NewGrepTool(workDir))
	r.Register(NewLsTool(workDir))
	r.Register(NewViewTool(workDir))

	// Write tools (require permission)
	r.Register(NewBashTool(workDir))
	r.Register(NewWriteTool(workDir))
	r.Register(NewEditTool(workDir))

	// Network tools
	r.Register(NewFetchTool())

	return r
}
