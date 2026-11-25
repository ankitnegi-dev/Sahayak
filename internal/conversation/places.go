package conversation

import (
	"strings"

	"github.com/ankitnegi-dev/Sahayak/internal/store"
)

// placeAliases maps common words to canonical place labels.
//
// The canonical label is what the user's saved_places row uses. The alias map
// lets "home" resolve to a place saved as "ghar", and "temple" to "mandir".
// It is intentionally small: a large alias map would silently guess, and
// guessing an address is the thing this product must never do.
var placeAliases = map[string]string{
	"home":     "ghar",
	"ghar":     "ghar",
	"house":    "ghar",
	"temple":   "mandir",
	"mandir":   "mandir",
	"mander":   "mandir",
	"hospital": "hospital",
	"office":   "office",
	"work":     "office",
	"station":  "station",
	"airport":  "airport",
	"market":   "market",
	"bazaar":   "market",
	"school":   "school",
	"college":  "college",
}

// ResolvePlace resolves a free-text place name to a saved place.
//
// It returns the saved place if found, or ErrUnknownPlace if not. It never
// guesses: an unknown name is an error, and the engine asks the user.
func ResolvePlace(name string, places []store.SavedPlace) (*store.SavedPlace, error) {
	canonical := canonicalPlaceName(name)
	if canonical == "" {
		return nil, ErrUnknownPlace
	}

	for i := range places {
		if strings.EqualFold(places[i].Label, canonical) {
			return &places[i], nil
		}
	}

	// Try the raw name too, in case the user has a custom label.
	for i := range places {
		if strings.EqualFold(places[i].Label, strings.TrimSpace(name)) {
			return &places[i], nil
		}
	}

	return nil, ErrUnknownPlace
}

// canonicalPlaceName normalises a place name to its canonical form.
func canonicalPlaceName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	if canonical, ok := placeAliases[name]; ok {
		return canonical
	}
	return name
}

// ResolvePlaceAddress returns the address string for a resolved place,
// or the original name if no saved place exists. This is used for display
// only, never for booking without a saved place.
func ResolvePlaceAddress(name string, places []store.SavedPlace) string {
	if p, err := ResolvePlace(name, places); err == nil && p != nil {
		if p.Address != "" {
			return p.Address
		}
	}
	return name
}

// PlaceLabel returns the display label for a place.
func PlaceLabel(p *store.SavedPlace) string {
	if p == nil {
		return ""
	}
	return p.Label
}
