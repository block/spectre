package lower

import . "github.com/alecthomas/types/optional"

func Name() string { return "a.B" }

func Pair() (string, error) { return "a.B", nil }

func Lookup(name string) bool { return name != "" }

func Count(kind int) bool { return kind > 0 }

func Labels(labels ...string) int { return len(labels) }

func Maybe(name Option[string]) {}

func Any(value any) {}
