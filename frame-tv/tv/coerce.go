package tv

import (
	"strconv"
	"strings"
)

// Samsung's art API is loosely typed: the same field can arrive as a JSON
// number, a quoted string, or a bool depending on firmware. These helpers
// coerce those values without panicking.

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}

func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

// trimDotLower lowercases an extension and drops a leading dot ("./JPG" style
// inputs aren't expected, but ".JPG" / "JPG" both normalize to "jpg").
func trimDotLower(ext string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	case float64:
		return x != 0
	default:
		return false
	}
}
