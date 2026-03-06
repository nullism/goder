package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OpenAIClientID is the OAuth client ID used by OpenCode for ChatGPT OAuth.
const OpenAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const (
	openAIAuthBaseURL       = "https://auth.openai.com"
	openAIDeviceAuthURL     = openAIAuthBaseURL + "/api/accounts/deviceauth/usercode"
	openAIDeviceTokenURL    = openAIAuthBaseURL + "/api/accounts/deviceauth/token"
	openAITokenExchangeURL  = openAIAuthBaseURL + "/oauth/token"
	openAIDeviceCallbackURI = openAIAuthBaseURL + "/deviceauth/callback"
	openAICallbackPort      = 1455
)

var (
	openAIBrowserMu      sync.Mutex
	openAIBrowserServer  *http.Server
	openAIBrowserPending *openAIBrowserPendingFlow
)

type openAIBrowserPendingFlow struct {
	state    string
	verifier string
	resultCh chan openAIBrowserResult
}

type openAIBrowserResult struct {
	token *OpenAIToken
	err   error
}

// OpenAIDeviceCodeResponse is returned when initiating OpenAI device auth.
type OpenAIDeviceCodeResponse struct {
	DeviceAuthID string
	UserCode     string
	Interval     int
}

type openAIDeviceCodeRaw struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     string `json:"interval"`
}

type openAIDeviceTokenRaw struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// OpenAIToken contains OAuth tokens and metadata.
type OpenAIToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	AccountID    string
}

type openAITokenRaw struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type jwtClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Auth             struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
	Organizations []struct {
		ID string `json:"id"`
	} `json:"organizations"`
}

// RequestOpenAIDeviceCode starts OpenAI device auth and returns a user code.
func RequestOpenAIDeviceCode(ctx context.Context) (*OpenAIDeviceCodeResponse, error) {
	body := strings.NewReader(fmt.Sprintf(`{"client_id":"%s"}`, OpenAIClientID))
	req, err := http.NewRequestWithContext(ctx, "POST", openAIDeviceAuthURL, body)
	if err != nil {
		return nil, fmt.Errorf("creating openai device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "goder/0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting openai device code: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading openai device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai device code request failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var raw openAIDeviceCodeRaw
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return nil, fmt.Errorf("parsing openai device code response: %w", err)
	}
	if raw.DeviceAuthID == "" || raw.UserCode == "" {
		return nil, fmt.Errorf("invalid openai device code response: missing required fields")
	}

	interval := 5
	if raw.Interval != "" {
		if n, err := time.ParseDuration(raw.Interval + "s"); err == nil && n > 0 {
			interval = int(n.Seconds())
		}
	}

	return &OpenAIDeviceCodeResponse{
		DeviceAuthID: raw.DeviceAuthID,
		UserCode:     raw.UserCode,
		Interval:     interval,
	}, nil
}

// PollOpenAIForToken polls OpenAI until device auth is completed or cancelled.
func PollOpenAIForToken(ctx context.Context, deviceAuthID, userCode string, interval int) (*OpenAIToken, error) {
	if interval <= 0 {
		interval = 5
	}
	pollInterval := time.Duration(interval)*time.Second + 3*time.Second

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		token, done, err := pollOpenAIDeviceTokenOnce(ctx, deviceAuthID, userCode)
		if err != nil {
			return nil, err
		}
		if done {
			return token, nil
		}
	}
}

// StartOpenAIBrowserAuth initializes PKCE browser auth and returns the auth URL.
func StartOpenAIBrowserAuth() (string, error) {
	if err := ensureOpenAIBrowserServer(); err != nil {
		return "", err
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", err
	}
	state, err := randomBase64URL(32)
	if err != nil {
		return "", fmt.Errorf("generating openai oauth state: %w", err)
	}

	flow := &openAIBrowserPendingFlow{
		state:    state,
		verifier: verifier,
		resultCh: make(chan openAIBrowserResult, 1),
	}

	openAIBrowserMu.Lock()
	if openAIBrowserPending != nil {
		openAIBrowserMu.Unlock()
		return "", fmt.Errorf("openai auth already in progress")
	}
	openAIBrowserPending = flow
	openAIBrowserMu.Unlock()

	redirectURI := openAICallbackURL()
	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {OpenAIClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"opencode"},
		"state":                      {state},
	}

	return openAIAuthBaseURL + "/oauth/authorize?" + params.Encode(), nil
}

// WaitOpenAIBrowserAuth blocks until browser auth completes or is cancelled.
func WaitOpenAIBrowserAuth(ctx context.Context) (*OpenAIToken, error) {
	openAIBrowserMu.Lock()
	flow := openAIBrowserPending
	openAIBrowserMu.Unlock()

	if flow == nil {
		return nil, fmt.Errorf("no openai browser auth in progress")
	}

	select {
	case result := <-flow.resultCh:
		return result.token, result.err
	case <-ctx.Done():
		cancelOpenAIBrowserAuth(ctx.Err())
		return nil, ctx.Err()
	}
}

func ensureOpenAIBrowserServer() error {
	openAIBrowserMu.Lock()
	defer openAIBrowserMu.Unlock()

	if openAIBrowserServer != nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", openAICallbackPort))
	if err != nil {
		return fmt.Errorf("starting openai oauth callback server on port %d: %w", openAICallbackPort, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", handleOpenAICallback)
	openAIBrowserServer = &http.Server{Handler: mux}

	go func() {
		_ = openAIBrowserServer.Serve(ln)
	}()

	return nil
}

func handleOpenAICallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errCode := r.URL.Query().Get("error")
	errDesc := r.URL.Query().Get("error_description")

	openAIBrowserMu.Lock()
	flow := openAIBrowserPending
	if flow == nil {
		openAIBrowserMu.Unlock()
		writeOpenAIAuthPage(w, false, "No auth request in progress")
		return
	}
	if state == "" || state != flow.state {
		openAIBrowserMu.Unlock()
		writeOpenAIAuthPage(w, false, "Invalid auth state")
		return
	}
	openAIBrowserPending = nil
	openAIBrowserMu.Unlock()

	if errCode != "" {
		detail := errCode
		if errDesc != "" {
			detail = errDesc
		}
		flow.resultCh <- openAIBrowserResult{err: fmt.Errorf("openai oauth failed: %s", detail)}
		writeOpenAIAuthPage(w, false, detail)
		return
	}

	if code == "" {
		flow.resultCh <- openAIBrowserResult{err: fmt.Errorf("openai oauth callback missing code")}
		writeOpenAIAuthPage(w, false, "Missing authorization code")
		return
	}

	tok, err := exchangeOpenAIAuthorizationCode(r.Context(), code, flow.verifier, openAICallbackURL())
	if err != nil {
		flow.resultCh <- openAIBrowserResult{err: err}
		writeOpenAIAuthPage(w, false, err.Error())
		return
	}

	flow.resultCh <- openAIBrowserResult{token: tok}
	writeOpenAIAuthPage(w, true, "")
}

func writeOpenAIAuthPage(w http.ResponseWriter, ok bool, details string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = io.WriteString(w, `<!doctype html><html><body style="font-family:sans-serif;padding:24px;"><h2>Authorization successful</h2><p>You can close this window and return to goder.</p><script>setTimeout(()=>window.close(),1800)</script></body></html>`)
		return
	}
	_, _ = io.WriteString(w, "<!doctype html><html><body style=\"font-family:sans-serif;padding:24px;\"><h2>Authorization failed</h2><p>"+htmlEscape(details)+"</p></body></html>")
}

func cancelOpenAIBrowserAuth(err error) {
	openAIBrowserMu.Lock()
	flow := openAIBrowserPending
	openAIBrowserPending = nil
	openAIBrowserMu.Unlock()

	if flow == nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	flow.resultCh <- openAIBrowserResult{err: err}
}

func openAICallbackURL() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", openAICallbackPort)
}

func generatePKCE() (string, string, error) {
	verifier, err := randomBase64URL(64)
	if err != nil {
		return "", "", fmt.Errorf("generating openai pkce verifier: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

func pollOpenAIDeviceTokenOnce(ctx context.Context, deviceAuthID, userCode string) (*OpenAIToken, bool, error) {
	body := strings.NewReader(fmt.Sprintf(`{"device_auth_id":"%s","user_code":"%s"}`, deviceAuthID, userCode))
	req, err := http.NewRequestWithContext(ctx, "POST", openAIDeviceTokenURL, body)
	if err != nil {
		return nil, false, fmt.Errorf("creating openai device token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "goder/0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("polling openai device token: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("reading openai device token response: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("openai device token poll failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var raw openAIDeviceTokenRaw
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return nil, false, fmt.Errorf("parsing openai device token response: %w", err)
	}
	if raw.AuthorizationCode == "" || raw.CodeVerifier == "" {
		return nil, false, fmt.Errorf("invalid openai device token response: missing authorization fields")
	}

	tok, err := exchangeOpenAIAuthorizationCode(ctx, raw.AuthorizationCode, raw.CodeVerifier, openAIDeviceCallbackURI)
	if err != nil {
		return nil, false, err
	}
	return tok, true, nil
}

// RefreshOpenAIToken refreshes an OpenAI OAuth access token.
func RefreshOpenAIToken(ctx context.Context, refreshToken string) (*OpenAIToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {OpenAIClientID},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", openAITokenExchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating openai refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refreshing openai access token: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading openai refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai refresh failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	tok, err := parseOpenAITokenResponse(bodyBytes)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func exchangeOpenAIAuthorizationCode(ctx context.Context, code, codeVerifier, redirectURI string) (*OpenAIToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {OpenAIClientID},
		"code_verifier": {codeVerifier},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", openAITokenExchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating openai token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging openai authorization code: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading openai token exchange response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai token exchange failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return parseOpenAITokenResponse(bodyBytes)
}

func parseOpenAITokenResponse(body []byte) (*OpenAIToken, error) {
	var raw openAITokenRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing openai token response: %w", err)
	}
	if raw.AccessToken == "" || raw.RefreshToken == "" {
		return nil, fmt.Errorf("invalid openai token response: missing tokens")
	}
	expiresIn := raw.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	accountID := extractAccountIDFromJWT(raw.IDToken)
	if accountID == "" {
		accountID = extractAccountIDFromJWT(raw.AccessToken)
	}

	return &OpenAIToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresIn:    expiresIn,
		AccountID:    accountID,
	}, nil
}

func extractAccountIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if claims.ChatGPTAccountID != "" {
		return claims.ChatGPTAccountID
	}
	if claims.Auth.ChatGPTAccountID != "" {
		return claims.Auth.ChatGPTAccountID
	}
	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID
	}
	return ""
}
