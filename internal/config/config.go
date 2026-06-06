package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type AccessLevel string
type Transport string

const (
	ReadOnly AccessLevel = "READONLY"
	DMLRW    AccessLevel = "DML-RW"
	DDLRW    AccessLevel = "DDL-RW"

	StdioTransport Transport = "stdio"
	SSETransport   Transport = "sse"
)

type Config struct {
	AccessLevel            AccessLevel
	Server                 string
	Port                   int
	Database               string
	Username               string
	Password               string
	TrustServerCertificate bool
	ConnectionTimeout      time.Duration
	QueryTimeout           time.Duration
	MaxRowsDefault         int
	RequireConfirmation    bool
	Transport              Transport
	HTTPAddr               string
	SSEPath                string
}

func ParseAccessLevel(s string) (AccessLevel, error) {
	switch AccessLevel(strings.ToUpper(strings.TrimSpace(s))) {
	case "":
		return ReadOnly, nil
	case ReadOnly:
		return ReadOnly, nil
	case DMLRW:
		return DMLRW, nil
	case DDLRW:
		return DDLRW, nil
	default:
		return "", fmt.Errorf("invalid MSSQL_ACCESS_LEVEL %q, expected READONLY, DML-RW, or DDL-RW", s)
	}
}

func ParseTransport(s string) (Transport, error) {
	switch Transport(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return StdioTransport, nil
	case StdioTransport:
		return StdioTransport, nil
	case SSETransport:
		return SSETransport, nil
	default:
		return "", fmt.Errorf("invalid MSSQL_TRANSPORT %q, expected stdio or sse", s)
	}
}

func (l AccessLevel) AllowsDML() bool {
	return l == DMLRW || l == DDLRW
}

func (l AccessLevel) AllowsDDL() bool {
	return l == DDLRW
}

func Load() (Config, error) {
	level, err := ParseAccessLevel(os.Getenv("MSSQL_ACCESS_LEVEL"))
	if err != nil {
		return Config{}, err
	}
	transport, err := ParseTransport(os.Getenv("MSSQL_TRANSPORT"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AccessLevel:            level,
		Server:                 os.Getenv("MSSQL_SERVER"),
		Database:               os.Getenv("MSSQL_DATABASE"),
		Username:               os.Getenv("MSSQL_USERNAME"),
		Password:               os.Getenv("MSSQL_PASSWORD"),
		Port:                   intEnv("MSSQL_PORT", 1433),
		TrustServerCertificate: boolEnv("MSSQL_TRUST_SERVER_CERTIFICATE", false),
		ConnectionTimeout:      durationSecondsEnv("MSSQL_CONNECTION_TIMEOUT", 30*time.Second),
		QueryTimeout:           durationSecondsEnv("MSSQL_QUERY_TIMEOUT", 120*time.Second),
		MaxRowsDefault:         intEnv("MSSQL_MAX_ROWS_DEFAULT", 1000),
		RequireConfirmation:    boolEnv("MSSQL_REQUIRE_CONFIRMATION", true),
		Transport:              transport,
		HTTPAddr:               stringEnv("MSSQL_HTTP_ADDR", ":8080"),
		SSEPath:                stringEnv("MSSQL_SSE_PATH", "/sse"),
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("MSSQL_SERVER is required")
	}
	if c.Database == "" {
		return fmt.Errorf("MSSQL_DATABASE is required")
	}
	if c.Username == "" {
		return fmt.Errorf("MSSQL_USERNAME is required")
	}
	if c.Password == "" {
		return fmt.Errorf("MSSQL_PASSWORD is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("MSSQL_PORT must be between 1 and 65535")
	}
	if c.ConnectionTimeout <= 0 {
		return fmt.Errorf("MSSQL_CONNECTION_TIMEOUT must be positive")
	}
	if c.QueryTimeout <= 0 {
		return fmt.Errorf("MSSQL_QUERY_TIMEOUT must be positive")
	}
	if c.MaxRowsDefault <= 0 || c.MaxRowsDefault > 100000 {
		return fmt.Errorf("MSSQL_MAX_ROWS_DEFAULT must be between 1 and 100000")
	}
	if c.Transport != StdioTransport && c.Transport != SSETransport {
		return fmt.Errorf("MSSQL_TRANSPORT must be stdio or sse")
	}
	if c.HTTPAddr == "" {
		return fmt.Errorf("MSSQL_HTTP_ADDR is required")
	}
	if !strings.HasPrefix(c.SSEPath, "/") {
		return fmt.Errorf("MSSQL_SSE_PATH must start with /")
	}
	return nil
}

func (c Config) ConnectionString() string {
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(c.Username, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Server, c.Port),
	}
	q := u.Query()
	q.Set("database", c.Database)
	q.Set("connection timeout", strconv.Itoa(int(c.ConnectionTimeout.Seconds())))
	q.Set("encrypt", "true")
	q.Set("TrustServerCertificate", strconv.FormatBool(c.TrustServerCertificate))
	u.RawQuery = q.Encode()
	return u.String()
}

func (c Config) PublicSummary() map[string]any {
	return map[string]any{
		"accessLevel":            c.AccessLevel,
		"server":                 c.Server,
		"port":                   c.Port,
		"database":               c.Database,
		"usernameConfigured":     c.Username != "",
		"passwordConfigured":     c.Password != "",
		"trustServerCertificate": c.TrustServerCertificate,
		"connectionTimeoutSec":   int(c.ConnectionTimeout.Seconds()),
		"queryTimeoutSec":        int(c.QueryTimeout.Seconds()),
		"maxRowsDefault":         c.MaxRowsDefault,
		"requireConfirmation":    c.RequireConfirmation,
		"transport":              c.Transport,
		"httpAddr":               c.HTTPAddr,
		"ssePath":                c.SSEPath,
	}
}

func stringEnv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func intEnv(name string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func boolEnv(name string, fallback bool) bool {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func durationSecondsEnv(name string, fallback time.Duration) time.Duration {
	return time.Duration(intEnv(name, int(fallback.Seconds()))) * time.Second
}
