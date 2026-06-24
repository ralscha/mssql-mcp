package config

import (
	"net/url"
	"testing"
	"time"
)

func TestParseAccessLevel(t *testing.T) {
	tests := []struct {
		in   string
		want AccessLevel
		ok   bool
	}{
		{"", ReadOnly, true},
		{"readonly", ReadOnly, true},
		{"DML-RW", DMLRW, true},
		{"ddl-rw", DDLRW, true},
		{"admin", "", false},
	}
	for _, tt := range tests {
		got, err := ParseAccessLevel(tt.in)
		if tt.ok && err != nil {
			t.Fatalf("ParseAccessLevel(%q) unexpected error: %v", tt.in, err)
		}
		if !tt.ok && err == nil {
			t.Fatalf("ParseAccessLevel(%q) expected error", tt.in)
		}
		if got != tt.want {
			t.Fatalf("ParseAccessLevel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseTransport(t *testing.T) {
	tests := []struct {
		in   string
		want Transport
		ok   bool
	}{
		{"", StdioTransport, true},
		{"stdio", StdioTransport, true},
		{"SSE", SSETransport, true},
		{"http", "", false},
	}
	for _, tt := range tests {
		got, err := ParseTransport(tt.in)
		if tt.ok && err != nil {
			t.Fatalf("ParseTransport(%q) unexpected error: %v", tt.in, err)
		}
		if !tt.ok && err == nil {
			t.Fatalf("ParseTransport(%q) expected error", tt.in)
		}
		if got != tt.want {
			t.Fatalf("ParseTransport(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAccessLevelPermissions(t *testing.T) {
	if ReadOnly.AllowsDML() || ReadOnly.AllowsDDL() {
		t.Fatal("READONLY should not allow writes")
	}
	if !DMLRW.AllowsDML() || DMLRW.AllowsDDL() {
		t.Fatal("DML-RW should allow only DML writes")
	}
	if !DDLRW.AllowsDML() || !DDLRW.AllowsDDL() {
		t.Fatal("DDL-RW should allow DML and DDL writes")
	}
}

func TestValidateTransportConfig(t *testing.T) {
	cfg := Config{
		AccessLevel:         ReadOnly,
		Server:              "localhost",
		Port:                1433,
		Database:            "db",
		Username:            "sa",
		Password:            "password",
		ConnectionTimeout:   1,
		QueryTimeout:        1,
		MaxRowsDefault:      1,
		Transport:           SSETransport,
		HTTPAddr:            ":8080",
		SSEPath:             "/sse",
		RequireConfirmation: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}

	cfg.SSEPath = "sse"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for SSE path without leading slash")
	}
}

func TestLoadEncryptDefaultsTrue(t *testing.T) {
	t.Setenv("MSSQL_SERVER", "localhost")
	t.Setenv("MSSQL_DATABASE", "db")
	t.Setenv("MSSQL_USERNAME", "sa")
	t.Setenv("MSSQL_PASSWORD", "password")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.Encrypt != "true" {
		t.Fatalf("Load() MSSQL_ENCRYPT = %q, want true", cfg.Encrypt)
	}
}

func TestLoadEncryptString(t *testing.T) {
	t.Setenv("MSSQL_SERVER", "localhost")
	t.Setenv("MSSQL_DATABASE", "db")
	t.Setenv("MSSQL_USERNAME", "sa")
	t.Setenv("MSSQL_PASSWORD", "password")
	t.Setenv("MSSQL_ENCRYPT", "strict")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.Encrypt != "strict" {
		t.Fatalf("Load() MSSQL_ENCRYPT = %q, want strict", cfg.Encrypt)
	}
}

func TestConnectionStringEncrypt(t *testing.T) {
	cfg := Config{
		Server:            "localhost",
		Port:              1433,
		Database:          "db",
		Username:          "sa",
		Password:          "password",
		Encrypt:           "disable",
		ConnectionTimeout: 30 * time.Second,
	}

	u, err := url.Parse(cfg.ConnectionString())
	if err != nil {
		t.Fatalf("ConnectionString() returned invalid URL: %v", err)
	}
	if got := u.Query().Get("encrypt"); got != "disable" {
		t.Fatalf("ConnectionString() encrypt = %q, want disable", got)
	}
}
