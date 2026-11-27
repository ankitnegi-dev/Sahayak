package llm

import "strings"

func containsAny(text string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func extractPlaces(text string) (pickup, drop string) {
	// Pattern: "X se Y" or "X se Y jaana"
	if idx := strings.Index(text, " se "); idx >= 0 {
		pickup = strings.TrimSpace(text[:idx])
		rest := strings.TrimSpace(text[idx+4:])
		// Remove trailing verbs
		for _, verb := range []string{"jaana hai", "jaana", "aana hai", "aana", "chahiye", "book karo", "book"} {
			rest = strings.TrimSuffix(rest, verb)
			rest = strings.TrimSpace(rest)
		}
		if rest != "" {
			drop = rest
		}
	}

	// Pattern: "from X to Y"
	if pickup == "" && drop == "" {
		if idx := strings.Index(text, " from "); idx >= 0 {
			rest := text[idx+6:]
			if toIdx := strings.Index(rest, " to "); toIdx >= 0 {
				pickup = strings.TrimSpace(rest[:toIdx])
				drop = strings.TrimSpace(rest[toIdx+4:])
			}
		}
	}

	// Pattern: "pickup X, drop Y"
	if pickup == "" && drop == "" {
		if idx := strings.Index(text, "pickup "); idx >= 0 {
			rest := text[idx+7:]
			if commaIdx := strings.Index(rest, ","); commaIdx >= 0 {
				pickup = strings.TrimSpace(rest[:commaIdx])
				dropPart := strings.TrimSpace(rest[commaIdx+1:])
				dropPart = strings.TrimPrefix(dropPart, "drop ")
				drop = strings.TrimSpace(dropPart)
			}
		}
	}

	return pickup, drop
}
