package scaffold

import (
	"strings"
	"text/template"

	"github.com/alecthomas/errors"
)

// templateFuncs are the functions templates can call, beyond Go's built-ins.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"split": split,
	}
}

// split returns the comma-separated items of a value, trimmed, without empty ones.
func split(value string) []string {
	items := []string{}
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
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
