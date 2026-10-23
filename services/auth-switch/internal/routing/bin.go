package routing

import "strings"

// Route maps a BIN prefix to an issuer endpoint.
type Route struct {
	Prefix   string
	IssuerID string
	URL      string
}

// Table does longest-prefix matching on BINs.
type Table struct{ routes []Route }

func DefaultTable() *Table {
	return &Table{routes: []Route{
		{Prefix: "411111", IssuerID: "issuer-a", URL: "http://issuer-sim:8082"},
		{Prefix: "5555", IssuerID: "issuer-b", URL: "http://issuer-sim:8082"},
		{Prefix: "4", IssuerID: "issuer-a", URL: "http://issuer-sim:8082"},
	}}
}

// Lookup returns the route with the longest matching prefix.
func (t *Table) Lookup(bin string) (Route, bool) {
	var best Route
	found := false
	for _, r := range t.routes {
		if strings.HasPrefix(bin, r.Prefix) && len(r.Prefix) > len(best.Prefix) {
			best, found = r, true
		}
	}
	return best, found
}
