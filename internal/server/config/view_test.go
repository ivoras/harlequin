package config

import (
	"fmt"
	"strings"
	"testing"
)

// server.yaml names .env variables; a secret written into it is refused.
func TestInlineSecretsRefused(t *testing.T) {
	for _, c := range []struct {
		name, yaml, want string // want "" = accepted
	}{
		{"password key", "email:\n  smtp_password: hunter2\n", "email.smtp_password looks like a secret"},
		{"api key", "web_search:\n  api_key: abc\n", "web_search.api_key looks like a secret"},
		{"token in a list", "x:\n  - bot_token: 1:A\n", "x[0].bot_token looks like a secret"},
		{"value in an _env key", "telegram:\n  bot_token_env: 123:ABC-def\n", "telegram.bot_token_env must name an environment variable"},
		{"URL password", "s:\n  base_url: https://u:p@h/v1\n", "s.base_url has a password"},
		{"URL key parameter", "s:\n  base_url: https://h/v1?api_key=x\n", `s.base_url has a "api_key" parameter`},
		{"env names", "email:\n  smtp_password_env: SMTP_PASSWORD\ntelegram:\n  bot_token_env: TG_TOKEN\n", ""},
		{"token counts", "sessions:\n  log_tokens: true\nagent:\n  compaction:\n    threshold_tokens: 5\n", ""},
		{"empty secret key", "web_search:\n  api_key: \"\"\n", ""},
		{"URL with user only", "s:\n  base_url: https://u@h/v1?model=m\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := rejectInlineSecrets([]byte(c.yaml))
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// ctx.config reads the effective config as server.yaml spells it, and the
// secrets loaded from the environment are not in it.
func TestViewLookupWithoutSecrets(t *testing.T) {
	secrets := map[string]string{"JWT_SECRET": "s-jwt", "LLM_API_KEY": "s-llm", "EMBED_API_KEY": "s-emb",
		"BRAVE_API_KEY": "s-brave", "ZYTE_API_KEY": "s-zyte", "SMTP_PASSWORD": "s-smtp", "TELEGRAM_BOT_TOKEN": "s-tg"}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	c, err := Load("../../../configs/server.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v := c.View()
	for key, want := range map[string]any{
		"agent.skill_tools_upfront": false,
		"sessions.idle_timeout":     "30m0s",
		"agent.no_such_key":         nil,
		"agent.max_steps.deeper":    nil,
	} {
		if got := Lookup(v, key); got != want {
			t.Errorf("Lookup(%q) = %#v, want %#v", key, got, want)
		}
	}
	all := fmt.Sprint(v)
	for name, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("view contains the value of %s", name)
		}
	}
}
