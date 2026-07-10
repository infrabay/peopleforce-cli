package httpx

import (
	"fmt"
	"net/url"
	"strings"
)

// QueryPair is one query key/value. Keys are written to the wire verbatim:
// the PeopleForce API uses literal bracket names ("employee_ids[]",
// "hired_on[gte]") that Rails parses positionally — percent-encoding the
// key or sanitizing it breaks filtering, so url.Values is not usable here.
type QueryPair struct {
	Key   string
	Value string
}

// EncodeQuery serializes pairs in order. Keys go out verbatim (brackets,
// underscores untouched); values are percent-encoded. Repeatable params
// are expressed by passing multiple pairs with the same key:
//
//	employee_ids[]=1&employee_ids[]=2
func EncodeQuery(pairs []QueryPair) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.Key)
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

// SanitizeRequestTarget makes a user-supplied path?query legal for an HTTP
// request line without touching what must stay verbatim. Spaces, control
// bytes, non-ASCII, and other request-line-illegal characters are
// percent-encoded; lone % becomes %25. Legal URL characters — including the
// literal [ ] of bracket params, /, ?, &, = — and valid %XX escapes pass
// through unchanged. # is encoded too: for an API CLI it is data, not a
// fragment delimiter (net/url would otherwise silently drop everything
// after it).
func SanitizeRequestTarget(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			if i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
				b.WriteByte(c)
			} else {
				b.WriteString("%25")
			}
		case c > 0x20 && c < 0x7f && !strings.ContainsRune("\"<>{}|\\^`#", rune(c)):
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
