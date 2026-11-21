package config

import (
	"fmt"
	"net"
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
	HTTPTransport  Transport = "http"
)

type Config struct {
	AccessLevel            AccessLevel
	Server                 string
	Port                   int
	Database               string
	Username               string
	Password               string
	Encrypt                string
	TrustServerCertificate bool
	ConnectionTimeout      time.Duration
	QueryTimeout           time.Duration
	MaxRowsDefault         int
	RequireConfirmation    bool
	Transport              Transport
	HTTPAddr               string
	HTTPPath               string
	HTTPBearerToken        string
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
	case HTTPTransport:
		return HTTPTransport, nil
	default:
		return "", fmt.Errorf("invalid MSSQL_TRANSPORT %q, expected stdio or http", s)
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
	server := strings.TrimSpace(os.Getenv("MSSQL_SERVER"))
	defaultPort := 1433
	if strings.Contains(server, `\`) && strings.TrimSpace(os.Getenv("MSSQL_PORT")) == "" {
		defaultPort = 0
	}
	port, err := intEnv("MSSQL_PORT", defaultPort)
	if err != nil {
		return Config{}, err
	}
	trustServerCertificate, err := boolEnv("MSSQL_TRUST_SERVER_CERTIFICATE", false)
	if err != nil {
		return Config{}, err
	}
	connectionTimeout, err := durationSecondsEnv("MSSQL_CONNECTION_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	queryTimeout, err := durationSecondsEnv("MSSQL_QUERY_TIMEOUT", 120*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxRowsDefault, err := intEnv("MSSQL_MAX_ROWS_DEFAULT", 1000)
	if err != nil {
		return Config{}, err
	}
	requireConfirmation, err := boolEnv("MSSQL_REQUIRE_CONFIRMATION", true)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AccessLevel:            level,
		Server:                 server,
		Database:               strings.TrimSpace(os.Getenv("MSSQL_DATABASE")),
		Username:               strings.TrimSpace(os.Getenv("MSSQL_USERNAME")),
		Password:               os.Getenv("MSSQL_PASSWORD"),
		Port:                   port,
		Encrypt:                stringEnv("MSSQL_ENCRYPT", "true"),
		TrustServerCertificate: trustServerCertificate,
		ConnectionTimeout:      connectionTimeout,
		QueryTimeout:           queryTimeout,
		MaxRowsDefault:         maxRowsDefault,
		RequireConfirmation:    requireConfirmation,
		Transport:              transport,
		HTTPAddr:               stringEnv("MSSQL_HTTP_ADDR", "127.0.0.1:8080"),
		HTTPPath:               stringEnv("MSSQL_HTTP_PATH", "/mcp"),
		HTTPBearerToken:        strings.TrimSpace(os.Getenv("MSSQL_HTTP_BEARER_TOKEN")),
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.AccessLevel != ReadOnly && c.AccessLevel != DMLRW && c.AccessLevel != DDLRW {
		return fmt.Errorf("MSSQL_ACCESS_LEVEL must be READONLY, DML-RW, or DDL-RW")
	}
	_, instance, err := splitServerInstance(c.Server)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Database) == "" {
		return fmt.Errorf("MSSQL_DATABASE is required")
	}
	if strings.TrimSpace(c.Username) == "" {
		return fmt.Errorf("MSSQL_USERNAME is required")
	}
	if c.Password == "" {
		return fmt.Errorf("MSSQL_PASSWORD is required")
	}
	if (instance == "" || c.Port != 0) && (c.Port <= 0 || c.Port > 65535) {
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
	switch strings.ToLower(strings.TrimSpace(c.Encrypt)) {
	case "mandatory", "yes", "1", "t", "true", "disable", "strict", "optional", "no", "0", "f", "false":
	default:
		return fmt.Errorf("MSSQL_ENCRYPT has an unsupported value %q", c.Encrypt)
	}
	if c.Transport != StdioTransport && c.Transport != HTTPTransport {
		return fmt.Errorf("MSSQL_TRANSPORT must be stdio or http")
	}
	if c.Transport == HTTPTransport {
		if strings.TrimSpace(c.HTTPAddr) == "" {
			return fmt.Errorf("MSSQL_HTTP_ADDR is required for HTTP transport")
		}
		if c.HTTPPath == "" || !strings.HasPrefix(c.HTTPPath, "/") || strings.ContainsAny(c.HTTPPath, "{}?#\t\r\n ") {
			return fmt.Errorf("MSSQL_HTTP_PATH must be an exact path starting with /")
		}
	}
	return nil
}

func (c Config) ConnectionString() string {
	host, instance, err := splitServerInstance(c.Server)
	if err != nil {
		host = strings.TrimSpace(c.Server)
	}
	var hostPort string
	switch {
	case c.Port > 0:
		hostPort = net.JoinHostPort(host, strconv.Itoa(c.Port))
	case strings.Contains(host, ":"):
		hostPort = "[" + host + "]"
	default:
		hostPort = host
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(c.Username, c.Password),
		Host:   hostPort,
	}
	if instance != "" {
		u.Path = "/" + instance
	}
	q := u.Query()
	q.Set("database", c.Database)
	q.Set("connection timeout", strconv.Itoa(int(c.ConnectionTimeout.Seconds())))
	q.Set("encrypt", c.Encrypt)
	q.Set("TrustServerCertificate", strconv.FormatBool(c.TrustServerCertificate))
	u.RawQuery = q.Encode()
	return u.String()
}

func splitServerInstance(raw string) (host, instance string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("MSSQL_SERVER is required")
	}
	host = trimmed
	if before, after, hasInstance := strings.Cut(trimmed, `\`); hasInstance {
		host = strings.TrimSpace(before)
		instance = strings.TrimSpace(after)
		if host == "" || instance == "" || strings.Contains(instance, `\`) {
			return "", "", fmt.Errorf("invalid MSSQL_SERVER %q", raw)
		}
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if host == "" {
			return "", "", fmt.Errorf("invalid MSSQL_SERVER %q", raw)
		}
	}
	return host, instance, nil
}

// HTTPListensBeyondLoopback reports whether the HTTP transport accepts
// connections from other hosts. Hostnames other than localhost count as
// non-loopback because they may resolve to a routable address.
func (c Config) HTTPListensBeyondLoopback() bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(c.HTTPAddr))
	if err != nil {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func (c Config) PublicSummary() map[string]any {
	return map[string]any{
		"accessLevel":            c.AccessLevel,
		"server":                 c.Server,
		"port":                   c.Port,
		"database":               c.Database,
		"usernameConfigured":     c.Username != "",
		"passwordConfigured":     c.Password != "",
		"encrypt":                c.Encrypt,
		"trustServerCertificate": c.TrustServerCertificate,
		"connectionTimeoutSec":   int(c.ConnectionTimeout.Seconds()),
		"queryTimeoutSec":        int(c.QueryTimeout.Seconds()),
		"maxRowsDefault":         c.MaxRowsDefault,
		"requireConfirmation":    c.RequireConfirmation,
		"transport":              c.Transport,
		"httpAddr":               c.HTTPAddr,
		"httpPath":               c.HTTPPath,
		"httpAuthConfigured":     c.HTTPBearerToken != "",
	}
}

func stringEnv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func intEnv(name string, fallback int) (int, error) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", name, err)
		}
		return n, nil
	}
	return fallback, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%s must be a boolean: %w", name, err)
		}
		return b, nil
	}
	return fallback, nil
}

func durationSecondsEnv(name string, fallback time.Duration) (time.Duration, error) {
	seconds, err := intEnv(name, int(fallback.Seconds()))
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}
