package provider

import "fmt"

// Supported returns the list of provider identifiers supported by this build.
func Supported() []string {
	return []string{"openai"}
}

// New creates a provider instance for the given provider name.
func New(name, apiKey, model string) (Provider, error) {
	switch name {
	case "openai":
		return NewOpenAIProvider(apiKey, model), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (supported: openai)", name)
	}
}
