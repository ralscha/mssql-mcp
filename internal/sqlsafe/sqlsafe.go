package sqlsafe

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxIdentifierRunes = 128

var (
	// database/sql rejects named arguments that do not begin with a letter.
	paramNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_@$#]*$`)
	parameterRE = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_@$#]*`)
)

var forbiddenReadOnlyWords = map[string]struct{}{
	"ADD": {}, "ALTER": {}, "BACKUP": {}, "BEGIN": {}, "BULK": {}, "CHECKPOINT": {},
	"CLOSE": {}, "COMMIT": {}, "CONVERSATION": {}, "CREATE": {}, "DBCC": {}, "DEALLOCATE": {},
	"DECLARE": {}, "DELETE": {}, "DENY": {}, "DISABLE": {}, "DROP": {},
	"ENABLE": {}, "EXEC": {}, "EXECUTE": {}, "GOTO": {}, "GRANT": {},
	"IF": {}, "INSERT": {}, "INTO": {}, "KILL": {}, "MERGE": {},
	"OPEN": {}, "OPENDATASOURCE": {}, "OPENQUERY": {}, "OPENROWSET": {},
	"PRINT": {}, "RAISERROR": {}, "READTEXT": {}, "RECEIVE": {},
	"RECONFIGURE": {}, "RESTORE": {}, "RETURN": {}, "REVERT": {},
	"REVOKE": {}, "ROLLBACK": {}, "SAVE": {}, "SEND": {}, "SET": {}, "SETUSER": {},
	"SHUTDOWN": {}, "TABLOCKX": {}, "THROW": {}, "TRUNCATE": {},
	"UPDATE": {}, "UPDATETEXT": {}, "UPDLOCK": {}, "USE": {},
	"WAITFOR": {}, "WHILE": {}, "WRITETEXT": {}, "XLOCK": {},
}

func QuoteIdentifier(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	if utf8.RuneCountInString(name) > maxIdentifierRunes {
		return "", fmt.Errorf("identifier %q exceeds %d characters", name, maxIdentifierRunes)
	}
	if strings.Contains(name, "--") || strings.Contains(name, "/*") || strings.Contains(name, "*/") {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	for _, r := range name {
		if !utf8.ValidRune(r) || unicode.IsControl(r) || strings.ContainsRune("[];'\"", r) {
			return "", fmt.Errorf("invalid identifier %q", name)
		}
	}
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

// QuoteRawIdentifier quotes an identifier read from SQL Server metadata,
// escaping embedded closing brackets while rejecting empty or overlong names.
func QuoteRawIdentifier(name string) (string, error) {
	if name == "" || utf8.RuneCountInString(name) > maxIdentifierRunes || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

func QuoteMultipart(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("invalid multipart identifier %q", name)
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 || len(parts) > 2 {
		return "", fmt.Errorf("invalid multipart identifier %q: expected table or schema.table", name)
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
// CTEs) and rejects T-SQL constructs that can mutate data, hold exclusive
// locks, or stack additional statements with or without semicolons.
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

	tokens, err := scanTokensWithDepth(code)
	if err != nil {
		return err
	}
	if len(tokens) == 0 || (tokens[0].upper != "SELECT" && tokens[0].upper != "WITH") {
		return fmt.Errorf("query must be a SELECT statement")
	}

	seenTopSelect := false
	prevTopUpper := ""
	prevPrevTopUpper := ""

	for i, tok := range tokens {
		if _, forbidden := forbiddenReadOnlyWords[tok.upper]; forbidden {
			return fmt.Errorf("%s is not allowed in a read-only query", tok.upper)
		}
		if tok.upper == "NEXT" && i+2 < len(tokens) && tokens[i+1].upper == "VALUE" && tokens[i+2].upper == "FOR" {
			return fmt.Errorf("NEXT VALUE FOR is not allowed in a read-only query")
		}
		if tok.depth == 0 {
			if tok.upper == ")" && !seenTopSelect && tokens[0].upper != "WITH" {
				seenTopSelect = true
			} else if tok.upper == "SELECT" {
				if !seenTopSelect {
					seenTopSelect = true
				} else {
					validSetOp := prevTopUpper == "UNION" || prevTopUpper == "INTERSECT" || prevTopUpper == "EXCEPT" ||
						(prevTopUpper == "ALL" && prevPrevTopUpper == "UNION")
					if !validSetOp {
						return fmt.Errorf("multiple SQL statements are not allowed")
					}
				}
			}
			prevPrevTopUpper = prevTopUpper
			prevTopUpper = tok.upper
		}
	}
	if !seenTopSelect {
		return fmt.Errorf("query must be a SELECT statement")
	}
	return nil
}

type depthToken struct {
	upper string
	depth int
}

func scanTokensWithDepth(code string) ([]depthToken, error) {
	var tokens []depthToken
	depth := 0
	for i := 0; i < len(code); {
		ch := code[i]
		switch {
		case ch == '(':
			depth++
			i++
		case ch == ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced parentheses in query")
			}
			if depth == 0 {
				tokens = append(tokens, depthToken{upper: ")", depth: 0})
			}
			i++
		case isTokenStart(ch):
			start := i
			i++
			for i < len(code) && isTokenContinue(code[i]) {
				i++
			}
			tokens = append(tokens, depthToken{
				upper: strings.ToUpper(code[start:i]),
				depth: depth,
			})
		case ch == ',' || ch == '+' || ch == '-' || ch == '*' || ch == '/' || ch == '=' || ch == '<' || ch == '>' || ch == '!':
			// Punctuation at depth 0 breaks adjacency for UNION [ALL] SELECT checks.
			if depth == 0 {
				tokens = append(tokens, depthToken{upper: string(ch), depth: 0})
			}
			i++
		default:
			i++
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses in query")
	}
	return tokens, nil
}

func isTokenStart(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_' || b == '@' || b == '#' || b == '$'
}

func isTokenContinue(b byte) bool {
	return isTokenStart(b) || (b >= '0' && b <= '9')
}

func IsReadOnlyQuery(query string) bool {
	return ValidateReadOnlyQuery(query) == nil
}

// ValidateWhereClause ensures a DML WHERE expression cannot stack statements,
// inject top-level set operators (UNION/INTERSECT/EXCEPT), or append query
// clauses (ORDER BY/GROUP BY/HAVING/OPTION/FOR) outside subqueries.
func ValidateWhereClause(where string) error {
	where = strings.TrimSpace(where)
	if where == "" {
		return fmt.Errorf("where is required")
	}
	if err := ValidateReadOnlyQuery("SELECT * FROM x WHERE (\n" + where + "\n)"); err != nil {
		return err
	}
	code, err := codeOnly(where)
	if err != nil {
		return err
	}
	tokens, err := scanTokensWithDepth(code)
	if err != nil {
		return err
	}
	for _, tok := range tokens {
		if tok.depth != 0 {
			continue
		}
		switch tok.upper {
		case "SELECT", "UNION", "INTERSECT", "EXCEPT", "ORDER", "GROUP", "HAVING", "OPTION", "FOR", "OFFSET", "FETCH":
			return fmt.Errorf("%s is not allowed at the top level of a WHERE clause", tok.upper)
		}
	}
	return nil
}

// NormalizeValue converts JSON-decoded numbers (such as whole-number float64
// values from map[string]any) to exact Go scalar types so go-mssqldb binds
// integer parameters as BIGINT instead of FLOAT(53), and rejects nested arrays
// or objects that database/sql cannot bind.
func NormalizeValue(value any) (any, error) {
	const maxExactFloatInt = 1 << 53
	switch v := value.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("non-finite floating-point value is not supported")
		}
		if math.Trunc(v) == v && v >= -maxExactFloatInt && v <= maxExactFloatInt {
			return int64(v), nil
		}
		return v, nil
	case float32:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite floating-point value is not supported")
		}
		return v, nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		if f, err := v.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, nil
		}
		return v.String(), nil
	case map[string]any, []any:
		return nil, fmt.Errorf("composite JSON values (arrays or objects) are not supported as SQL parameters")
	default:
		return v, nil
	}
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
		normalized, err := NormalizeValue(value)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		values[key] = normalized
		originalNames[key] = name
	}

	used := make(map[string]bool, len(params))
	args := make([]any, 0, len(params))
	for _, location := range parameterRE.FindAllStringIndex(code, -1) {
		if location[0] > 0 && isTokenContinue(code[location[0]-1]) {
			continue // System variable (e.g. @@VERSION) or @ embedded inside an identifier.
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
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `*`, `%`)
	if !strings.Contains(s, "%") {
		s = "%" + s + "%"
	}
	return s
}

func parameterNameValid(name string) bool {
	return paramNameRE.MatchString(name)
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
			switch {
			case query[i] == '/' && i+1 < len(out) && query[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				blockDepth++
			case query[i] == '*' && i+1 < len(out) && query[i+1] == '/':
				out[i], out[i+1] = ' ', ' '
				i++
				blockDepth--
				if blockDepth == 0 {
					state = normal
				}
			default:
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
