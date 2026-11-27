package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// View is the effective config (defaults applied) as server.yaml spells it,
// for templates' ctx.config. It holds no secrets: every secret field is
// `yaml:"-"`, and Load refuses a server.yaml that writes one inline.
func (c *Config) View() map[string]any {
	var m map[string]any
	if b, err := yaml.Marshal(c); err == nil {
		_ = yaml.Unmarshal(b, &m)
	}
	return m
}

// Lookup returns the value at a dotted key ("agent.skill_tools_upfront") in a
// View, or nil when there is none.
func Lookup(view map[string]any, key string) any {
	var cur any = view
	for part := range strings.SplitSeq(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

// secretName matches key and URL parameter names that carry a secret. Whole
// words only: "tokens" (log_tokens, threshold_tokens) counts tokens.
var secretName = regexp.MustCompile(`(?i)(^|_)(password|passwd|secret|token|key|api_?key|credentials?)($|_)`)

// envName is what a *_env key must hold: the name of an environment variable.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rejectInlineSecrets refuses a server.yaml that holds a secret itself rather
// than the name of the .env variable that does: a secret-named key with a
// value, a URL with a password or a key/token parameter, or an *_env key whose
// value is not a variable name (the secret pasted there by mistake).
func rejectInlineSecrets(raw []byte) error {
	var doc any
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil // the real decode reports it
	}
	return walkSecrets(doc, "")
}

func walkSecrets(v any, path string) error {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			s, isStr := val.(string)
			switch {
			case strings.HasSuffix(strings.ToLower(k), "_env"):
				if isStr && s != "" && !envName.MatchString(s) {
					return fmt.Errorf("config: %s must name an environment variable (set in .env), not hold its value", p)
				}
			case isStr && s != "" && secretName.MatchString(k):
				return fmt.Errorf("config: %s looks like a secret; put it in .env and name the variable in %s_env", p, k)
			}
			if err := walkSecrets(val, p); err != nil {
				return err
			}
		}
	case []any:
		for i, val := range x {
			if err := walkSecrets(val, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case string:
		u, err := url.Parse(x)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil
		}
		if _, ok := u.User.Password(); ok {
			return fmt.Errorf("config: %s has a password in its URL; put it in .env", path)
		}
		for q := range u.Query() {
			if secretName.MatchString(q) {
				return fmt.Errorf("config: %s has a %q parameter in its URL; put the secret in .env", path, q)
			}
		}
	}
	return nil
}
