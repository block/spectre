package scaffold

import (
	"bytes"
	"encoding/json"
	"strings"
	"text/template"

	"github.com/alecthomas/errors"
)

// templateFuncs are the functions templates can call, beyond Go's built-ins.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"toml":       tomlValue,
		"join":       func(sep string, items []string) string { return strings.Join(items, sep) },
		"replace":    func(old, replacement, text string) string { return strings.ReplaceAll(text, old, replacement) },
		"trimPrefix": func(prefix, text string) string { return strings.TrimPrefix(text, prefix) },
		"upper":      strings.ToUpper,
		"lower":      strings.ToLower,
	}
}

// tomlValue writes a string, boolean, or list of strings as a TOML value.
func tomlValue(value any) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	switch value := value.(type) {
	case string, bool:
		if err := encoder.Encode(value); err != nil {
			return "", errors.WithStack(err)
		}
	case []string:
		quoted := make([]string, 0, len(value))
		for _, item := range value {
			text, err := tomlValue(item)
			if err != nil {
				return "", err
			}
			quoted = append(quoted, text)
		}
		return "[" + strings.Join(quoted, ", ") + "]", nil
	default:
		return "", errors.Errorf("cannot write %T as TOML", value)
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// renderText renders a template string, failing on any value it does not have.
func renderText(name, text string, values map[string]any) (string, error) {
	parsed, err := template.New(name).Option("missingkey=error").Funcs(templateFuncs()).Parse(text)
	if err != nil {
		return "", errors.Wrapf(err, "parse %s", name)
	}
	var out strings.Builder
	if err := parsed.Execute(&out, values); err != nil {
		return "", errors.Wrapf(err, "render %s", name)
	}
	return out.String(), nil
}
