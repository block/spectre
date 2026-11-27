package http

import (
	nethttp "net/http"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
)

const baggageHeader = "Baggage"

// splitBaggage separates a header's baggage members from the values of its SPECTRE request ID members.
func splitBaggage(header nethttp.Header) (others, requestIDs []string) {
	for _, line := range header.Values(baggageHeader) {
		for member := range strings.SplitSeq(line, ",") {
			member = strings.TrimSpace(member)
			if member == "" {
				continue
			}
			key, value, _ := strings.Cut(member, "=")
			if strings.TrimSpace(key) != engine.RequestIDKey {
				others = append(others, member)
				continue
			}
			value, _, _ = strings.Cut(value, ";")
			requestIDs = append(requestIDs, strings.TrimSpace(value))
		}
	}
	return others, requestIDs
}

func setBaggage(header nethttp.Header, members []string) {
	if len(members) == 0 {
		header.Del(baggageHeader)
		return
	}
	header.Set(baggageHeader, strings.Join(members, ","))
}

// setRequestID replaces any SPECTRE request ID in a header's baggage with id.
func setRequestID(header nethttp.Header, id engine.RequestID) {
	others, _ := splitBaggage(header)
	setBaggage(header, append(others, engine.RequestIDKey+"="+id.String()))
}

// removeRequestID removes every SPECTRE request ID from a header's baggage and returns their
// values. A header without one is left untouched.
func removeRequestID(header nethttp.Header) (values []string) {
	others, values := splitBaggage(header)
	if len(values) > 0 {
		setBaggage(header, others)
	}
	return values
}

// takeRequestID removes the SPECTRE request ID from a header's baggage and returns it. Errors
// never quote the value, because quarantine reasons are reported outside the mirror.
func takeRequestID(header nethttp.Header) (Option[engine.RequestID], error) {
	values := removeRequestID(header)
	if len(values) == 0 {
		return None[engine.RequestID](), nil
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return None[engine.RequestID](), errors.New("baggage carries conflicting SPECTRE request IDs")
		}
	}
	id, err := typeid.Parse[engine.RequestID](values[0])
	if err != nil {
		return None[engine.RequestID](), errors.New("baggage carries a malformed SPECTRE request ID")
	}
	return Some(id), nil
}
