package transport

import "testing"

func TestParseBasicURL(t *testing.T) {
	u, err := ParseURI("quack://example.com", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "example.com" {
		t.Errorf("host: got %q", u.Host)
	}
	if u.Port != 9494 {
		t.Errorf("port: got %d", u.Port)
	}
	if u.TLS {
		t.Errorf("tls: expected false")
	}
}

func TestParseFullURL(t *testing.T) {
	u, err := ParseURI("quack://h.example:1234/mydb?token=abc&tls=true", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "h.example" || u.Port != 1234 {
		t.Errorf("host:port = %s:%d", u.Host, u.Port)
	}
	if u.Database != "mydb" {
		t.Errorf("database: got %q", u.Database)
	}
	if u.Token != "abc" {
		t.Errorf("token: got %q", u.Token)
	}
	if !u.TLS {
		t.Errorf("tls: expected true")
	}
	if want := "https://h.example:1234/quack"; u.HTTPURL() != want {
		t.Errorf("httpURL: got %q want %q", u.HTTPURL(), want)
	}
}

func TestOptsOverrideParams(t *testing.T) {
	u, err := ParseURI("quack://h:9494", map[string]string{"token": "from-opts"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Token != "from-opts" {
		t.Errorf("token: got %q", u.Token)
	}
}

func TestAcceptsURL(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"quack://localhost", true},
		{"jdbc:quack://localhost", true},
		{"http://example.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := AcceptsURL(c.s); got != c.want {
			t.Errorf("AcceptsURL(%q): got %v want %v", c.s, got, c.want)
		}
	}
}

func TestIPv6Host(t *testing.T) {
	u, err := ParseURI("quack://[::1]:9494", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "::1" {
		t.Errorf("host: got %q", u.Host)
	}
	if want := "http://[::1]:9494/quack"; u.HTTPURL() != want {
		t.Errorf("httpURL: got %q want %q", u.HTTPURL(), want)
	}
}
