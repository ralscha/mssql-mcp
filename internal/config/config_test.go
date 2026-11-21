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
		{"HTTP", HTTPTransport, true},
		{"sse", "", false},
		{"websocket", "", false},
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
		Encrypt:             "true",
		ConnectionTimeout:   1,
		QueryTimeout:        1,
		MaxRowsDefault:      1,
		Transport:           HTTPTransport,
		HTTPAddr:            ":8080",
		HTTPPath:            "/mcp",
		RequireConfirmation: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}

	cfg.HTTPPath = "mcp"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for HTTP path without leading slash")
	}
	cfg.HTTPPath = "/mcp/{session}"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for HTTP wildcard path")
	}
}

func TestLoadEncryptDefaultsTrue(t *testing.T) {
	setRequiredEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.Encrypt != "true" {
		t.Fatalf("Load() MSSQL_ENCRYPT = %q, want true", cfg.Encrypt)
	}
}

func TestLoadEncryptString(t *testing.T) {
	setRequiredEnvironment(t)
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

func TestConnectionStringIPv6(t *testing.T) {
	for _, host := range []string{"2001:db8::1", "[2001:db8::1]"} {
		cfg := Config{
			Server:            host,
			Port:              1433,
			Database:          "db",
			Username:          "sa",
			Password:          "password",
			Encrypt:           "true",
			ConnectionTimeout: 30 * time.Second,
		}
		u, err := url.Parse(cfg.ConnectionString())
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Host; got != "[2001:db8::1]:1433" {
			t.Fatalf("host(%q) = %q", host, got)
		}
	}
}

func TestConnectionStringNamedInstance(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("MSSQL_SERVER", `dbhost\SQLEXPRESS`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.Port != 0 {
		t.Fatalf("Port = %d, want 0 for named instance without MSSQL_PORT", cfg.Port)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr = %q, want 127.0.0.1:8080", cfg.HTTPAddr)
	}
	u, err := url.Parse(cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "dbhost" || u.Path != "/SQLEXPRESS" {
		t.Fatalf("url host=%q path=%q", u.Host, u.Path)
	}
}

func TestHTTPListensBeyondLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:8080": false,
		"[::1]:8080":     false,
		"localhost:8080": false,
		":8080":          true,
		"0.0.0.0:8080":   true,
		"[::]:8080":      true,
		"10.0.0.5:8080":  true,
		"myhost:8080":    true,
	}
	for addr, want := range tests {
		if got := (Config{HTTPAddr: addr}).HTTPListensBeyondLoopback(); got != want {
			t.Errorf("HTTPListensBeyondLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestLoadRejectsMalformedEnvironmentValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"MSSQL_PORT", "not-a-number"},
		{"MSSQL_CONNECTION_TIMEOUT", "soon"},
		{"MSSQL_QUERY_TIMEOUT", "later"},
		{"MSSQL_MAX_ROWS_DEFAULT", "many"},
		{"MSSQL_TRUST_SERVER_CERTIFICATE", "sometimes"},
		{"MSSQL_REQUIRE_CONFIRMATION", "perhaps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv(tt.name, tt.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidateRejectsInvalidEnums(t *testing.T) {
	cfg := validConfig()
	cfg.AccessLevel = "ADMIN"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid access-level error")
	}

	cfg = validConfig()
	cfg.Encrypt = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid encryption error")
	}
}

func TestValidateAllowsEmptyHTTPSettingsForStdio(t *testing.T) {
	cfg := validConfig()
	cfg.HTTPAddr = ""
	cfg.HTTPPath = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MSSQL_SERVER", "localhost")
	t.Setenv("MSSQL_DATABASE", "db")
	t.Setenv("MSSQL_USERNAME", "sa")
	t.Setenv("MSSQL_PASSWORD", "password")
	for _, name := range []string{
		"MSSQL_PORT", "MSSQL_CONNECTION_TIMEOUT", "MSSQL_QUERY_TIMEOUT", "MSSQL_MAX_ROWS_DEFAULT",
		"MSSQL_TRUST_SERVER_CERTIFICATE", "MSSQL_REQUIRE_CONFIRMATION", "MSSQL_ACCESS_LEVEL",
		"MSSQL_ENCRYPT", "MSSQL_TRANSPORT", "MSSQL_HTTP_ADDR", "MSSQL_HTTP_PATH", "MSSQL_HTTP_BEARER_TOKEN",
	} {
		t.Setenv(name, "")
	}
}

func validConfig() Config {
	return Config{
		AccessLevel:            ReadOnly,
		Server:                 "localhost",
		Port:                   1433,
		Database:               "db",
		Username:               "sa",
		Password:               "password",
		Encrypt:                "true",
		ConnectionTimeout:      time.Second,
		QueryTimeout:           time.Second,
		MaxRowsDefault:         1000,
		RequireConfirmation:    true,
		Transport:              StdioTransport,
		TrustServerCertificate: false,
	}
}
