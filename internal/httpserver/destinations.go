package httpserver

import (
	"cmp"
	"net/http/httputil"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/gobwas/glob"
)

// destinations selects the upstream of a request host: an exact host first, then
// the longest matching glob, with ties broken by the pattern.
type destinations struct {
	exact    map[string]*httputil.ReverseProxy
	patterns []destinationPattern
}

type destinationPattern struct {
	pattern string
	match   *glob.Pattern
	proxy   *httputil.ReverseProxy
}

func newDestinations() *destinations {
	return &destinations{exact: map[string]*httputil.ReverseProxy{}}
}

// add configures the upstream of a lower-case host or glob pattern.
func (d *destinations) add(host string, proxy *httputil.ReverseProxy) error {
	if !strings.ContainsAny(host, "*?[{") {
		d.exact[host] = proxy
		return nil
	}
	match, err := glob.Compile(host)
	if err != nil {
		return errors.Wrapf(err, "destination host %q is not a valid glob", host)
	}
	d.patterns = append(d.patterns, destinationPattern{pattern: host, match: match, proxy: proxy})
	slices.SortFunc(d.patterns, func(a, b destinationPattern) int {
		return cmp.Or(cmp.Compare(len(b.pattern), len(a.pattern)), cmp.Compare(a.pattern, b.pattern))
	})
	return nil
}

// lookup returns the upstream of a lower-case host name.
func (d *destinations) lookup(name string) (proxy *httputil.ReverseProxy, configured bool) {
	if proxy, configured = d.exact[name]; configured {
		return proxy, true
	}
	for _, pattern := range d.patterns {
		if pattern.match.Match(name) {
			return pattern.proxy, true
		}
	}
	return nil, false
}
