package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	if err := getJSON(ctx, hc, origin+"/.well-known/oauth-protected-resource", &prm); err == nil {
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
	for _, wk := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		if err := getJSON(ctx, hc, authServer+wk, &asm); err == nil && asm.AuthorizationEndpoint != "" {
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
		return "", "", fmt.Errorf("mcp: dynamic client registration failed: %s", resp.Status)
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
// token back to the user's database so rotations survive restarts.
type persistingTokenSource struct {
	base   oauth2.TokenSource
	reg    *Registry
	userDB *sql.DB
	scope  string
	name   string
	last   *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	if p.last == nil || tok.AccessToken != p.last.AccessToken || tok.RefreshToken != p.last.RefreshToken {
		_ = p.reg.SaveToken(context.Background(), p.userDB, p.scope, p.name, tok)
		p.last = tok
	}
	return tok, nil
}

// tokenSource returns an HTTP-client-ready TokenSource for a stored token that
// refreshes and persists automatically.
func tokenSource(ctx context.Context, hc *http.Client, reg *Registry, userDB *sql.DB, srv Server, tok *oauth2.Token) oauth2.TokenSource {
	cfg := oauthConfig(srv.OAuth, "")
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	return &persistingTokenSource{
		base:   cfg.TokenSource(ctx, tok),
		reg:    reg,
		userDB: userDB,
		scope:  srv.Scope,
		name:   srv.Name,
		last:   tok,
	}
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
