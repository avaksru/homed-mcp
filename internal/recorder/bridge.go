package recorder

import (
	"github.com/u236/homed-mcp/internal/homedweb"
)

// HomedwebLookup adapts a *homedweb.Provider to the NameLookup
// interface expected by the recorder package. The struct is the
// simplest possible adapter вЂ” every method just forwards the call
// and shapes the result. Defined in the recorder package (rather
// than the homedweb package) to keep the recorder package free of
// any homedweb import other than this single adapter type.
type HomedwebLookup struct {
	Provider *homedweb.Provider
}

// Lookup implements the NameLookup interface. It forwards to
// homedweb.Provider.Lookup and copies the homedweb.Match fields
// that the recorder cares about (dashboard / block / user-defined
// name / expose / property / endpoint) into a NameMatch.
func (h *HomedwebLookup) Lookup(endpoint, expose, property string) []NameMatch {
	if h == nil || h.Provider == nil {
		return nil
	}
	matches := h.Provider.Lookup(endpoint, expose, property)
	if len(matches) == 0 {
		return nil
	}
	out := make([]NameMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, NameMatch{
			Dashboard: m.Dashboard,
			Block:     m.Block,
			Name:      m.Item.Name,
			Expose:    m.Item.Expose,
			Property:  m.Item.Property,
			Endpoint:  m.Item.Endpoint,
		})
	}
	return out
}