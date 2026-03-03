package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubClientID is the OAuth client ID used for the device flow.
// This is the OpenCode OAuth App ID, which is allowlisted by GitHub
// for full Copilot model access (Claude, Gemini, GPT-5, Codex, etc.).
const GitHubClientID = "Ov23li8tweQw6odWQebz"

// DeviceCodeResponse contains the information returned by GitHub when
// requesting a device code for the OAuth device flow.
type DeviceCodeResponse struct {
	// DeviceCode is the code used to poll for an access token (not shown to user).
	DeviceCode string `json:"device_code"`

	// UserCode is the code the user enters at the verification URL.
	UserCode string `json:"user_code"`

	// VerificationURI is the URL where the user enters the code.
	VerificationURI string `json:"verification_uri"`

	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`

	// Interval is the minimum number of seconds between poll requests.
	Interval int `json:"interval"`
}

// tokenResponse is the response from polling for an access token.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

const copilotDeviceScope = "read:user repo"

// RequestDeviceCode initiates the GitHub OAuth device flow and returns
// the device code response containing the user code and verification URL.
func RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	form := url.Values{
		"client_id": {GitHubClientID},
		// repo scope is required for copilot_internal token exchange.
		"scope": {copilotDeviceScope},
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/device/code",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var dcResp DeviceCodeResponse
	if err := json.Unmarshal(body, &dcResp); err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}

	if dcResp.DeviceCode == "" || dcResp.UserCode == "" {
		return nil, fmt.Errorf("invalid device code response: missing required fields")
	}

	// Default interval to 5 seconds if not specified.
	if dcResp.Interval == 0 {
		dcResp.Interval = 5
	}

	return &dcResp, nil
}

// PollForToken polls GitHub's OAuth token endpoint until the user completes
// authorization, the device code expires, or the context is cancelled.
// It returns the OAuth access token on success.
func PollForToken(ctx context.Context, deviceCode string, interval int) (string, error) {
	// Add a safety margin to the poll interval per RFC 8628.
	pollInterval := time.Duration(interval+1) * time.Second

	form := url.Values{
		"client_id":   {GitHubClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		token, done, err := pollOnce(ctx, form)
		if err != nil {
			return "", err
		}
		if done {
			return token, nil
		}
	}
}

// pollOnce makes a single poll request. It returns (token, true, nil) on
// success, ("", false, nil) if still pending, or ("", false, err) on
// terminal errors.
func pollOnce(ctx context.Context, form url.Values) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("polling for token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, fmt.Errorf("reading token response: %w", err)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", false, fmt.Errorf("parsing token response: %w", err)
	}

	switch tokenResp.Error {
	case "":
		// Success
		if tokenResp.AccessToken == "" {
			return "", false, fmt.Errorf("received empty access token")
		}
		return tokenResp.AccessToken, true, nil
	case "authorization_pending":
		// User hasn't authorized yet; keep polling.
		return "", false, nil
	case "slow_down":
		// Server asked us to slow down; keep polling (our interval already
		// includes a safety margin).
		return "", false, nil
	case "expired_token":
		return "", false, fmt.Errorf("device code expired — please try again")
	case "access_denied":
		return "", false, fmt.Errorf("authorization denied by user")
	default:
		return "", false, fmt.Errorf("OAuth error: %s", tokenResp.Error)
	}
}
