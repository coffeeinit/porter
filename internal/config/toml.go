package config

import (
	"fmt"
	"strings"
)

// ParseTOML implements a deliberately small subset of TOML: comments
// (#), [section] headers, and `key = "value"` / `key = value` pairs,
// one per line. No arrays, inline tables, multi-line strings, or
// datetimes — porter.toml doesn't need them, and hand-rolling this
// subset avoids pulling in a TOML library. Returns section name -> key
// -> raw value; top-level (pre-section) keys live under the "" section.
func ParseTOML(text string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{"": {}}
	section := ""

	for lineNo, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("toml parse error at line %d: unterminated section header %q", lineNo+1, line)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("toml parse error at line %d: empty section name", lineNo+1)
			}
			if _, ok := out[section]; !ok {
				out[section] = map[string]string{}
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("toml parse error at line %d: expected `key = value`, got %q", lineNo+1, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("toml parse error at line %d: empty key", lineNo+1)
		}
		out[section][key] = unquoteTOML(strings.TrimSpace(val))
	}
	return out, nil
}

// stripTOMLComment removes a trailing `# comment`, but ignores `#`
// characters that appear inside a double-quoted string value.
func stripTOMLComment(line string) string {
	inQuotes := false
	for i, r := range line {
		switch r {
		case '"':
			inQuotes = !inQuotes
		case '#':
			if !inQuotes {
				return line[:i]
			}
		}
	}
	return line
}

func unquoteTOML(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		s := v[1 : len(v)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
		return s
	}
	// Bare values (true, 123, unquoted-string) are kept as-is; every
	// config field Porter reads is ultimately treated as a string.
	return v
}

// tomlGet reads sections[section][key], falling back to def if the
// section, key, or value is empty.
func tomlGet(sections map[string]map[string]string, section, key, def string) string {
	if s, ok := sections[section]; ok {
		if v, ok := s[key]; ok && v != "" {
			return v
		}
	}
	return def
}
