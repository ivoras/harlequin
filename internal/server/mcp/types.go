// Package mcp is the Harlequin MCP (Model Context Protocol) client. It lets the
// agent use external MCP servers as tool sources. Servers are registered at two
// scopes — "shared" (org-wide, in shared.db) and "user" (per-user, in user.db) —
// and authenticate either with a static header credential or via OAuth 2.1. All
// credentials are encrypted at rest (see internal/server/secrets); OAuth tokens
// are always per-user, even for shared servers.
//
// The package has three layers: a Registry (CRUD + token persistence), the OAuth
// 2.1 flow helpers, and a Manager that pools live connections and caches each
// server's tool list.
package mcp

const (
	// ScopeShared and ScopeUser are the two registration scopes.
	ScopeShared = "shared"
	ScopeUser   = "user"
)

// AuthType identifies how a server authenticates.
type AuthType string

const (
	AuthNone   AuthType = "none"   // no auth
	AuthHeader AuthType = "header" // static request header (bearer / API key)
	AuthOAuth  AuthType = "oauth"  // OAuth 2.1 (per-user tokens)
)

// Server is a decrypted, in-memory MCP server registration. Secret fields
// (HeaderValue, OAuth.ClientSecret) are populated only when the caller has the
// encryption key; they never leave the server process.
type Server struct {
	Scope     string // ScopeShared | ScopeUser
	Name      string
	URL       string
	Transport string // "http"
	AuthType  AuthType

	// Header auth.
	HeaderName  string
	HeaderValue string // decrypted; empty if no cipher

	// OAuth auth (non-secret config; ClientSecret decrypted separately).
	OAuth *OAuthMeta

	Enabled   bool
	CreatedBy int64
}

// OAuthMeta holds the non-secret OAuth client configuration discovered during
// registration. It is stored as JSON in the oauth_meta column. The client
// secret (if the provider issues one) is stored encrypted separately.
type OAuthMeta struct {
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint,omitempty"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ClientID              string   `json:"client_id,omitempty"`
	Scopes                []string `json:"scopes,omitempty"`
	// Resource is the canonical MCP server resource URI (RFC 8707), sent as the
	// `resource` parameter in authorization and token requests when present.
	Resource string `json:"resource,omitempty"`

	// ClientSecret is set in memory after decryption; it is NOT serialized to
	// oauth_meta (it lives in the encrypted auth_secret column).
	ClientSecret string `json:"-"`
}

// Tool is a tool advertised by an MCP server.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Status describes a server's current connectability for listings.
type Status struct {
	Enabled       bool
	AuthSatisfied bool   // true if we have the credentials needed to connect
	NeedsAuth     bool   // true if OAuth authorization is required but missing
	ToolCount     int    // number of tools (when connected during a status probe)
	Err           string // last connection/listing error, if any
}
