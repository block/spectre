package logger

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/errors"
)

// defaultJSON is the --log-json value selecting defaultTemplate, which matches
// slog's own JSON handler.
const (
	defaultJSON     = "DEFAULT"
	defaultTemplate = `{"time": "<time>", "level": "<level>", "msg": "<msg>"}`
)

// JSONTemplate is the JSON shape of SPECTRE's own log lines, so they match the logs
// of the service SPECTRE stands in for. The zero value writes text instead.
type JSONTemplate struct {
	root map[string]any
	// attrsPlaced records an <attrs> placeholder; without one, attributes go at the top level.
	attrsPlaced bool
}

// ParseJSONTemplate parses a template, or DEFAULT. Unknown placeholders are rejected
// so a typo fails at startup rather than producing odd logs.
func ParseJSONTemplate(text string) (JSONTemplate, error) {
	if text == defaultJSON {
		text = defaultTemplate
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return JSONTemplate{}, errors.Wrap(err, "log template must be a JSON object")
	}
	if decoder.More() {
		return JSONTemplate{}, errors.New("log template has data after its object")
	}
	// An empty template would be indistinguishable from the zero value, which writes text.
	if len(root) == 0 {
		return JSONTemplate{}, errors.New("log template must have at least one field")
	}
	attrsCount := 0
	if _, err := compile(root, &attrsCount); err != nil {
		return JSONTemplate{}, err
	}
	if attrsCount > 1 {
		return JSONTemplate{}, errors.New("log template places <attrs> more than once")
	}
	return JSONTemplate{root: root, attrsPlaced: attrsCount == 1}, nil
}

// UnmarshalText parses a template, so Kong validates --log-json as it parses flags.
func (t *JSONTemplate) UnmarshalText(text []byte) error {
	parsed, err := ParseJSONTemplate(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (t *JSONTemplate) enabled() bool {
	return len(t.root) > 0
}

// render fills in the template for record. Template keys take precedence over
// top-level attributes with the same name.
func (t *JSONTemplate) render(record slog.Record, attrs map[string]any) map[string]any {
	line := map[string]any{}
	if !t.attrsPlaced {
		maps.Copy(line, attrs)
	}
	for key, value := range t.root {
		line[key] = render(value, record, attrs)
	}
	return line
}

// placeholder is a parsed <name:argument> template value. Only the fields its name
// uses are set.
type placeholder struct {
	name   string
	format string
	levels levelNames
}

// render returns the placeholder's value for record. parsePlaceholder admits only
// msg, level, time and attrs.
func (p placeholder) render(record slog.Record, attrs map[string]any) any {
	switch p.name {
	case "msg":
		return record.Message
	case "level":
		return p.levels.render(record.Level)
	case "time":
		return formatTime(record.Time, p.format)
	}
	return attrs
}

// compile replaces placeholder strings in a decoded template value, counting
// <attrs> placeholders.
func compile(value any, attrsCount *int) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		for key, member := range value {
			compiled, err := compile(member, attrsCount)
			if err != nil {
				return nil, err
			}
			value[key] = compiled
		}
	case []any:
		for i, element := range value {
			compiled, err := compile(element, attrsCount)
			if err != nil {
				return nil, err
			}
			value[i] = compiled
		}
	case string:
		if opened, ok := strings.CutPrefix(value, "<"); ok {
			if inner, closed := strings.CutSuffix(opened, ">"); closed {
				return parsePlaceholder(inner, attrsCount)
			}
		}
	}
	return value, nil
}

// parsePlaceholder parses the text between a placeholder's angle brackets.
func parsePlaceholder(inner string, attrsCount *int) (placeholder, error) {
	name, argument, hasArgument := strings.Cut(inner, ":")
	switch name {
	case "msg", "attrs":
		if hasArgument {
			return placeholder{}, errors.Errorf("log template <%s> takes no argument", name)
		}
		if name == "attrs" {
			*attrsCount++
		}
		return placeholder{name: name}, nil
	case "level":
		levels, err := parseLevelNames(argument)
		return placeholder{name: name, levels: levels}, err
	case "time":
		return placeholder{name: name, format: argument}, nil
	}
	return placeholder{}, errors.Errorf("unknown log template placeholder <%s>", inner)
}

// render returns a template value with its placeholders filled in for record.
func render(value any, record slog.Record, attrs map[string]any) any {
	switch value := value.(type) {
	case placeholder:
		return value.render(record, attrs)
	case map[string]any:
		rendered := make(map[string]any, len(value))
		for key, member := range value {
			rendered[key] = render(member, record, attrs)
		}
		return rendered
	case []any:
		rendered := make([]any, len(value))
		for i, element := range value {
			rendered[i] = render(element, record, attrs)
		}
		return rendered
	}
	return value
}

// levelNames maps slog levels to a service's level names, ordered by level. When
// empty, slog's own names are used.
type levelNames []levelName

// levelName is a service's name for a level: a string, or a json.Number for loggers
// that write numeric levels.
type levelName struct {
	level slog.Level
	name  any
}

// parseLevelNames parses LEVEL=NAME pairs, such as warn=WARNING. When every name
// is an integer, names are written as JSON numbers.
func parseLevelNames(argument string) (levelNames, error) {
	names := levelNames{}
	if argument == "" {
		return names, nil
	}
	numeric := true
	for text := range strings.SplitSeq(argument, ",") {
		levelText, name, ok := strings.Cut(strings.TrimSpace(text), "=")
		if !ok || name == "" {
			return nil, errors.Errorf("log template level %q must be LEVEL=NAME", text)
		}
		var level slog.Level
		if err := level.UnmarshalText([]byte(levelText)); err != nil {
			return nil, errors.Wrapf(err, "log template level %q", text)
		}
		if slices.ContainsFunc(names, func(existing levelName) bool { return existing.level == level }) {
			return nil, errors.Errorf("log template names level %s more than once", level)
		}
		if _, err := strconv.ParseInt(name, 10, 64); err != nil {
			numeric = false
		}
		names = append(names, levelName{level: level, name: name})
	}
	slices.SortFunc(names, func(a levelName, b levelName) int { return int(a.level - b.level) })
	if numeric {
		for i := range names {
			names[i].name = json.Number(names[i].name.(string)) //nolint:forcetypeassert // set to a string above
		}
	}
	return names, nil
}

// render returns the name of the highest mapped level at or below level, or the
// lowest name when level is below them all.
func (n levelNames) render(level slog.Level) any {
	if len(n) == 0 {
		return level.String()
	}
	chosen := n[0]
	for _, name := range n {
		if name.level <= level {
			chosen = name
		}
	}
	return chosen.name
}

// formatTime formats at with a Go time layout or one of the names rfc3339,
// rfc3339nano, unix, unixms and unixns; an empty format means rfc3339nano.
func formatTime(at time.Time, format string) any {
	switch format {
	case "", "rfc3339nano":
		return at.Format(time.RFC3339Nano)
	case "rfc3339":
		return at.Format(time.RFC3339)
	case "unix":
		return at.Unix()
	case "unixms":
		return at.UnixMilli()
	case "unixns":
		return at.UnixNano()
	}
	return at.Format(format)
}
