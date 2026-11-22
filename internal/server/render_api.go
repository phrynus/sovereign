package server

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
)

// renderPageOrJSON returns page data as JSON for the console.
func (s *Server) renderPageOrJSON(c *gin.Context, data gin.H) {
	s.addUserToData(c, data)
	normalizeKeysToSnakeCase(data)
	c.JSON(http.StatusOK, data)
}

// toSnakeCase converts a PascalCase or camelCase string to snake_case.
func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if i > 0 {
			prev := rune(s[i-1])
			if unicode.IsUpper(r) {
				if unicode.IsLower(prev) {
					result = append(result, '_')
				} else if i+1 < len(s) && unicode.IsLower(rune(s[i+1])) {
					result = append(result, '_')
				}
			}
		}
		result = append(result, unicode.ToLower(r))
	}
	return strings.ToLower(string(result))
}

// normalizeKeysToSnakeCase renames every PascalCase page key to its snake_case
// form in place, keeping the snake_case value when a handler already set both.
//
// It used to *add* a snake_case alias next to the PascalCase key, which shipped
// every collection twice: the task list response was ~215 KB, about half of it
// duplicated arrays (Tasks, Agents, …). The console reads the snake_case keys,
// so the response now carries one key per field; the page handlers' PascalCase
// maps are an implementation detail, not a wire shape.
func normalizeKeysToSnakeCase(data gin.H) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	for _, k := range keys {
		snake := toSnakeCase(k)
		if snake == k {
			continue
		}
		if _, exists := data[snake]; !exists {
			data[snake] = data[k]
		}
		delete(data, k)
	}
}
