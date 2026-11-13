package config

import "testing"

func TestMarkupPercentValidation(t *testing.T) {
	for _, m := range []float64{-1, 1001} {
		c := &Config{JWTSecret: "x", ModelSuite: "y"}
		c.Server.OrgLLMMarkupPercent = m
		if err := c.validate(); err == nil {
			t.Errorf("markup %v accepted", m)
		}
	}
}
