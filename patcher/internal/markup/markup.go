// Package markup tokenises the renderer control sequences embedded in text-bank
// strings. The tokens drive branching/alternate selection and inline emphasis, so
// a patch must keep them intact unless a change is explicitly authorised.
package markup

import (
	"fmt"
	"regexp"
	"strings"
)

// inlineEmphasisRe normalises `@i…@/i`, `@b…@/b`, `@u…@/u`: the emphasis inner
// text is opaque, the pair itself is not.
var inlineEmphasisRe = regexp.MustCompile(`(@(?:i|b|u))[^@]*(@/(?:i|b|u))`)

func emphasisMask(s string) string {
	return inlineEmphasisRe.ReplaceAllString(s, "$1<INNER>$2")
}

// Tokens returns the ordered control-token sequence of s.
// Recognised tokens: `@#(n)`, `@/name`, `@name`, `@name(args)`, `##(args)`.
// Any other `@` form is an error (unknown token syntax).
func Tokens(s string) ([]string, error) {
	var out []string
	i, n := 0, len(s)
	for i < n {
		if s[i] == '@' {
			if i+1 >= n {
				return nil, fmt.Errorf("dangling '@' at byte %d", i)
			}
			c := s[i+1]
			switch {
			case c == '#':
				if i+2 < n && s[i+2] == '(' {
					j := strings.IndexByte(s[i+3:], ')')
					if j < 0 {
						return nil, fmt.Errorf("unterminated @#() token at byte %d", i)
					}
					j += i + 3
					out = append(out, s[i:j+1])
					i = j + 1
					continue
				}
				return nil, fmt.Errorf("malformed @# token at byte %d", i)
			case c == '/':
				j := i + 2
				for j < n && isASCIIAlpha(s[j]) {
					j++
				}
				if j == i+2 {
					return nil, fmt.Errorf("malformed @/ token at byte %d", i)
				}
				out = append(out, s[i:j])
				i = j
				continue
			case isASCIIAlpha(c):
				j := i + 1
				for j < n && isASCIIAlnum(s[j]) {
					j++
				}
				name := s[i:j]
				if j < n && s[j] == '(' {
					k := strings.IndexByte(s[j+1:], ')')
					if k < 0 {
						return nil, fmt.Errorf("unterminated %s( token at byte %d", name, i)
					}
					k += j + 1
					out = append(out, s[i:k+1])
					i = k + 1
					continue
				}
				out = append(out, name)
				i = j
				continue
			default:
				return nil, fmt.Errorf("unrecognised '@' token form %q at byte %d", s[i:min(i+8, n)], i)
			}
		}
		if i+3 <= n && s[i:i+3] == "##(" {
			j := strings.IndexByte(s[i+3:], ')')
			if j < 0 {
				return nil, fmt.Errorf("unterminated ##() token at byte %d", i)
			}
			j += i + 3
			out = append(out, s[i:j+1])
			i = j + 1
			continue
		}
		i++
	}
	return out, nil
}

func isASCIIAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isASCIIAlnum(b byte) bool { return isASCIIAlpha(b) || (b >= '0' && b <= '9') }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Equivalent reports whether new preserves the control-token sequence of orig.
func Equivalent(orig, new string) (bool, string) {
	to, err := Tokens(emphasisMask(orig))
	if err != nil {
		return false, "original: " + err.Error()
	}
	tn, err := Tokens(emphasisMask(new))
	if err != nil {
		return false, "new: " + err.Error()
	}
	if len(to) != len(tn) {
		return false, fmt.Sprintf("token count %d -> %d", len(to), len(tn))
	}
	for i := range to {
		if to[i] != tn[i] {
			return false, fmt.Sprintf("token %d changed: %q -> %q", i, to[i], tn[i])
		}
	}
	return true, ""
}
