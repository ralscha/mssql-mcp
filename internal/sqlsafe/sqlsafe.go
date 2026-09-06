package sqlsafe

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var (
	identifierRE = regexp.MustCompile(`^[A-Za-z_#@][A-Za-z0-9_@$#]*$`)
	parameterRE  = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_]*`)
	tokenRE      = regexp.MustCompile(`[A-Za-z_@#$][A-Za-z0-9_@#$]*`)
)

var forbiddenReadOnlyWords = map[string]struct{}{
	"ALTER": {}, "BACKUP": {}, "BEGIN": {}, "BULK": {}, "COMMIT": {},
	"CREATE": {}, "DBCC": {}, "DELETE": {}, "DENY": {}, "DROP": {},
	"EXEC": {}, "EXECUTE": {}, "GRANT": {}, "INSERT": {}, "INTO": {},
	"KILL": {}, "MERGE": {}, "RECONFIGURE": {}, "RESTORE": {}, "REVOKE": {},
	"OPENDATASOURCE": {}, "OPENQUERY": {}, "OPENROWSET": {},
	"ROLLBACK": {}, "SAVE": {}, "SET": {}, "SHUTDOWN": {}, "TRUNCATE": {},
	"UPDATE": {}, "USE": {}, "WAITFOR": {},
}

func QuoteIdentifier(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !identifierRE.MatchString(name) {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

func QuoteMultipart(name string) (string, error) {
	parts := strings.Split(strings.TrimSpace(name), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return "", fmt.Errorf("invalid multipart identifier %q", name)
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		q, err := QuoteIdentifier(part)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, "."), nil
}

// ValidateReadOnlyQuery accepts exactly one SELECT statement (optionally using
// CTEs) and rejects T-SQL constructs that can mutate data or session state.
// Quoted text, quoted identifiers, and comments are ignored while classifying
// the statement so harmless words such as 'DELETE' do not cause false rejects.
func ValidateReadOnlyQuery(query string) error {
	code, err := codeOnly(query)
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if strings.HasPrefix(code, ";") {
		code = strings.TrimSpace(code[1:])
		if !strings.HasPrefix(strings.ToUpper(code), "WITH") {
			return fmt.Errorf("only a leading semicolon before WITH is allowed")
		}
	}
	if strings.HasSuffix(code, ";") {
		code = strings.TrimSpace(code[:len(code)-1])
	}
	if code == "" {
		return fmt.Errorf("query is required")
	}
	if strings.Contains(code, ";") {
		return fmt.Errorf("multiple SQL statements are not allowed")
	}

	words := tokenRE.FindAllString(code, -1)
	if len(words) == 0 || (!strings.EqualFold(words[0], "SELECT") && !strings.EqualFold(words[0], "WITH")) {
		return fmt.Errorf("query must be a SELECT statement")
	}
	for i, word := range words {
		upper := strings.ToUpper(word)
		if _, forbidden := forbiddenReadOnlyWords[upper]; forbidden {
			return fmt.Errorf("%s is not allowed in a read-only query", upper)
		}
		if upper == "NEXT" && i+2 < len(words) && strings.EqualFold(words[i+1], "VALUE") && strings.EqualFold(words[i+2], "FOR") {
			return fmt.Errorf("NEXT VALUE FOR is not allowed in a read-only query")
		}
	}
	return nil
}

func IsReadOnlyQuery(query string) bool {
	return ValidateReadOnlyQuery(query) == nil
}

// BindNamedParameters validates every @name placeholder outside quoted text
// and comments, then returns database/sql named arguments. Parameter names are
// matched case-insensitively, as they are by SQL Server.
func BindNamedParameters(query string, params map[string]any) ([]any, error) {
	code, err := codeOnly(query)
	if err != nil {
		return nil, err
	}

	values := make(map[string]any, len(params))
	originalNames := make(map[string]string, len(params))
	for name, value := range params {
		if !parameterNameValid(name) {
			return nil, fmt.Errorf("invalid parameter name %q", name)
		}
		key := strings.ToLower(name)
		if previous, exists := originalNames[key]; exists {
			return nil, fmt.Errorf("parameter names %q and %q differ only by case", previous, name)
		}
		values[key] = value
		originalNames[key] = name
	}

	used := make(map[string]bool, len(params))
	args := make([]any, 0, len(params))
	for _, location := range parameterRE.FindAllStringIndex(code, -1) {
		if location[0] > 0 && code[location[0]-1] == '@' {
			continue // SQL Server system variable, for example @@VERSION.
		}
		name := code[location[0]+1 : location[1]]
		key := strings.ToLower(name)
		value, exists := values[key]
		if !exists {
			return nil, fmt.Errorf("query parameter @%s has no value", name)
		}
		if !used[key] {
			args = append(args, sql.Named(name, value))
			used[key] = true
		}
	}
	for key, name := range originalNames {
		if !used[key] {
			return nil, fmt.Errorf("parameter %q is not used by the query", name)
		}
	}
	return args, nil
}

func LikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	s = strings.ReplaceAll(s, `*`, `%`)
	if !strings.Contains(s, "%") {
		s = "%" + s + "%"
	}
	return s
}

func parameterNameValid(name string) bool {
	if name == "" || name[0] == '@' || name[0] == '#' {
		return false
	}
	return identifierRE.MatchString(name)
}

// codeOnly returns a same-length copy with comments and quoted content replaced
// by spaces. Keeping byte offsets intact lets callers locate parameters in the
// original query safely.
func codeOnly(query string) (string, error) {
	const (
		normal = iota
		singleQuote
		doubleQuote
		bracketQuote
		lineComment
		blockComment
	)

	out := []byte(query)
	state := normal
	blockDepth := 0
	for i := 0; i < len(out); i++ {
		switch state {
		case normal:
			switch {
			case out[i] == '-' && i+1 < len(out) && out[i+1] == '-':
				out[i], out[i+1] = ' ', ' '
				i++
				state = lineComment
			case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				blockDepth = 1
				state = blockComment
			case out[i] == '\'':
				out[i] = ' '
				state = singleQuote
			case out[i] == '"':
				out[i] = ' '
				state = doubleQuote
			case out[i] == '[':
				out[i] = ' '
				state = bracketQuote
			}
		case singleQuote:
			out[i] = ' '
			if query[i] == '\'' {
				if i+1 < len(out) && query[i+1] == '\'' {
					i++
					out[i] = ' '
				} else {
					state = normal
				}
			}
		case doubleQuote:
			out[i] = ' '
			if query[i] == '"' {
				if i+1 < len(out) && query[i+1] == '"' {
					i++
					out[i] = ' '
				} else {
					state = normal
				}
			}
		case bracketQuote:
			out[i] = ' '
			if query[i] == ']' {
				if i+1 < len(out) && query[i+1] == ']' {
					i++
					out[i] = ' '
				} else {
					state = normal
				}
			}
		case lineComment:
			if query[i] == '\n' || query[i] == '\r' {
				state = normal
			} else {
				out[i] = ' '
			}
		case blockComment:
			if query[i] == '/' && i+1 < len(out) && query[i+1] == '*' {
				out[i], out[i+1] = ' ', ' '
				i++
				blockDepth++
			} else if query[i] == '*' && i+1 < len(out) && query[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				blockDepth--
				if blockDepth == 0 {
					state = normal
				}
			} else {
				out[i] = ' '
			}
		}
	}

	switch state {
	case normal, lineComment:
		return string(out), nil
	case singleQuote:
		return "", fmt.Errorf("unterminated string literal")
	case doubleQuote:
		return "", fmt.Errorf("unterminated quoted identifier")
	case bracketQuote:
		return "", fmt.Errorf("unterminated bracketed identifier")
	default:
		return "", fmt.Errorf("unterminated block comment")
	}
}
