package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// discoverOAuth performs MCP OAuth metadata discovery for an MCP server URL: it
// finds the protected-resource's authorization server, then that server's
// authorization/token/registration endpoints. Endpoints already present in the
// supplied meta are kept (operator overrides). It does not perform client
// registration; see registerClient.
func discoverOAuth(ctx context.Context, hc *http.Client, serverURL string, meta *OAuthMeta) (*OAuthMeta, error) {
	if meta == nil {
		meta = &OAuthMeta{}
	}
	origin, err := originOf(serverURL)
	if err != nil {
		return nil, err
	}
	if meta.Resource == "" {
		meta.Resource = serverURL
	}

	// 1. Protected-resource metadata → authorization server(s).
	authServer := origin
	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
		Resource             string   `json:"resource"`
	}
	foundPRM := false
	for _, u := range wellKnown(serverURL, "oauth-protected-resource") {
		if err := getJSON(ctx, hc, u, &prm); err == nil {
			foundPRM = true
			break
		}
	}
	if foundPRM {
		if prm.Resource != "" {
			meta.Resource = prm.Resource
		}
		if len(prm.AuthorizationServers) > 0 {
			authServer = strings.TrimRight(prm.AuthorizationServers[0], "/")
		}
	}

	// 2. Authorization-server metadata (RFC 8414, with OIDC fallback).
	var asm struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	ok := false
	var candidates []string
	for _, wk := range []string{"oauth-authorization-server", "openid-configuration"} {
		candidates = append(candidates, wellKnown(authServer, wk)...)
	}
	// OIDC also allows the well-known suffix after the issuer path.
	candidates = append(candidates, authServer+"/.well-known/openid-configuration")
	for _, u := range candidates {
		if err := getJSON(ctx, hc, u, &asm); err == nil && asm.AuthorizationEndpoint != "" {
			ok = true
			break
		}
	}
	if ok {
		if meta.AuthorizationEndpoint == "" {
			meta.AuthorizationEndpoint = asm.AuthorizationEndpoint
		}
		if meta.TokenEndpoint == "" {
			meta.TokenEndpoint = asm.TokenEndpoint
		}
		if meta.RegistrationEndpoint == "" {
			meta.RegistrationEndpoint = asm.RegistrationEndpoint
		}
	}
	if !ok && !foundPRM {
		// No metadata at all: MCP 2025-03-26 default endpoints on the server origin.
		if meta.AuthorizationEndpoint == "" {
			meta.AuthorizationEndpoint = origin + "/authorize"
		}
		if meta.TokenEndpoint == "" {
			meta.TokenEndpoint = origin + "/token"
		}
		if meta.RegistrationEndpoint == "" {
			meta.RegistrationEndpoint = origin + "/register"
		}
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return meta, fmt.Errorf("mcp: could not discover OAuth endpoints for %s", serverURL)
	}
	return meta, nil
}

// registerClient performs OAuth 2.0 Dynamic Client Registration (RFC 7591) if a
// registration endpoint is known and no client_id is set yet. It returns the
// client_id and (optional) client_secret to persist.
func registerClient(ctx context.Context, hc *http.Client, meta *OAuthMeta, clientName, redirectURI string) (clientID, clientSecret string, err error) {
	if meta.ClientID != "" || meta.RegistrationEndpoint == "" {
		return meta.ClientID, meta.ClientSecret, nil
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none", // public client (PKCE)
		"scope":                      strings.Join(meta.Scopes, " "),
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Surface the server's error body — providers often explain (e.g. an
		// "invalid_redirect_uri" when DCR is restricted to specific clients).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var oe struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &oe) == nil && oe.Error == "invalid_redirect_uri" {
			host := meta.RegistrationEndpoint
			if u, err := url.Parse(host); err == nil {
				host = u.Host
			}
			return "", "", fmt.Errorf("mcp: %s only lets pre-approved apps sign in and rejected Harlequin's callback URL %s. "+
				"Ask the provider to allow that redirect URI, or use an API key if they offer one (provider said: %s)",
				host, redirectURI, oe.Description)
		}
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return "", "", fmt.Errorf("mcp: dynamic client registration failed (%s): %s", resp.Status, detail)
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		return "", "", err
	}
	if reg.ClientID == "" {
		return "", "", fmt.Errorf("mcp: registration response missing client_id")
	}
	return reg.ClientID, reg.ClientSecret, nil
}

// oauthConfig builds an oauth2.Config from discovered metadata.
func oauthConfig(meta *OAuthMeta, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     meta.ClientID,
		ClientSecret: meta.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint},
		RedirectURL:  redirectURL,
		Scopes:       meta.Scopes,
	}
}

// authCodeURL builds the authorization URL with PKCE and the optional resource
// indicator (RFC 8707).
func authCodeURL(meta *OAuthMeta, redirectURL, state, verifier string) string {
	cfg := oauthConfig(meta, redirectURL)
	opts := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier)}
	if meta.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", meta.Resource))
	}
	return cfg.AuthCodeURL(state, opts...)
}

// exchangeCode swaps an authorization code for tokens, completing PKCE.
func exchangeCode(ctx context.Context, hc *http.Client, meta *OAuthMeta, redirectURL, code, verifier string) (*oauth2.Token, error) {
	cfg := oauthConfig(meta, redirectURL)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	opts := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if meta.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", meta.Resource))
	}
	return cfg.Exchange(ctx, code, opts...)
}

// persistingTokenSource wraps an oauth2.TokenSource and writes any refreshed
// token back to the user's database so rotations survive restarts. It lives in
// a pooled session that outlives the dialing request, so it opens the user DB
// itself (via the Manager) rather than holding a request-scoped handle.
type persistingTokenSource struct {
	base   oauth2.TokenSource
	m      *Manager
	userID int64
	srv    Server
	mu     sync.Mutex
	last   *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		if grantLost(err) {
			p.m.authLost(p.userID, nil, p.srv, "refresh rejected: "+describeTokenErr(err))
		} else {
			log.Printf("mcp: token refresh for user %d %s/%s failed: %s", p.userID, p.srv.Scope, p.srv.Name, describeTokenErr(err))
		}
		return nil, err
	}
	p.mu.Lock()
	changed := p.last == nil || tok.AccessToken != p.last.AccessToken || tok.RefreshToken != p.last.RefreshToken
	p.last = tok
	p.mu.Unlock()
	if changed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.m.withUser(ctx, p.userID, func(udb *sql.DB) error {
			return p.m.reg.SaveToken(ctx, udb, p.srv.Scope, p.srv.Name, tok)
		}); err != nil {
			log.Printf("mcp: persist refreshed token for user %d %s/%s: %v", p.userID, p.srv.Scope, p.srv.Name, err)
		}
	}
	return tok, nil
}

// describeTokenErr renders a token-endpoint failure for the log: HTTP status,
// OAuth error code/description, and a short body excerpt (error bodies carry
// no credentials). Without this a dead sign-in is undiagnosable.
func describeTokenErr(err error) string {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return err.Error()
	}
	status := 0
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	body := strings.Join(strings.Fields(string(re.Body)), " ")
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return fmt.Sprintf("HTTP %d, error=%q, description=%q, body=%s", status, re.ErrorCode, re.ErrorDescription, body)
}

// grantLost reports whether a token-refresh failure means the sign-in is dead
// (revoked/expired refresh token) rather than a transient outage.
func grantLost(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	return re.ErrorCode == "invalid_grant" || (re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized)
}

// tokenSource returns an HTTP-client-ready TokenSource for a stored token that
// refreshes and persists automatically.
func tokenSource(ctx context.Context, hc *http.Client, m *Manager, userID int64, srv Server, tok *oauth2.Token) oauth2.TokenSource {
	cfg := oauthConfig(srv.OAuth, "")
	// The refresh runs later, inside pooled sessions: don't tie it to the
	// dialing request's context.
	ctx = context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, hc)
	// ReuseTokenSource serialises refreshes and only calls through when expired.
	return oauth2.ReuseTokenSource(tok, &persistingTokenSource{
		base:   cfg.TokenSource(ctx, tok),
		m:      m,
		userID: userID,
		srv:    srv,
		last:   tok,
	})
}

// wellKnown returns the RFC 8414/9728 metadata URLs for raw: the well-known
// segment inserted before the URL path first, then at the bare origin.
func wellKnown(raw, suffix string) []string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	root := origin + "/.well-known/" + suffix
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		return []string{root + p, root}
	}
	return []string{root}
}

// originOf returns scheme://host[:port] for a URL.
func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("mcp: invalid server URL %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func getJSON(ctx context.Context, hc *http.Client, url string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
