package session

import "testing"

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"CONNECT marin jv4c2hq7t3m6k9x1b8n5r0wzye": "CONNECT marin [key]",
		"connect marin secret\x01":                 "connect marin [key]",
		"CONNECT marin":                            "CONNECT marin",
		"CHAT GLOBAL hello":                        "CHAT GLOBAL hello",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
