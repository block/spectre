package scaffold

import (
	"strings"
	"text/template"

	"github.com/alecthomas/errors"
)

// templateFuncs are the functions templates can call, beyond Go's built-ins.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"join":       func(sep string, items []string) string { return strings.Join(items, sep) },
		"replace":    func(old, replacement, text string) string { return strings.ReplaceAll(text, old, replacement) },
		"trimPrefix": func(prefix, text string) string { return strings.TrimPrefix(text, prefix) },
		"upper":      strings.ToUpper,
		"lower":      strings.ToLower,
	}
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
