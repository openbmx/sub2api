// Package anthropicfp provides pure helpers for suppressing client-side
// fingerprints that would otherwise be visible to upstream Anthropic when a
// forwarding gateway sits between the client and api.anthropic.com.
//
// Currently exposes NormalizeDateline: it rewrites the "Today's date is
// YYYY-MM-DD." sentence inside a request body back to a canonical ASCII form,
// erasing three bits of steganographic signal (four apostrophe code points and
// a date-separator variant) that some clients embed in that sentence when
// they detect a non-official base URL.
package anthropicfp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// datelineRegexes matches the fingerprinted sentence with any of the four
// apostrophe code points seen in the wild and either separator. Two regexes
// are used because Go's RE2-based regexp package does not support
// backreferences: matching `-` and `/` in two passes keeps the two separators
// inside YYYY?MM?DD forced to agree, so mixed-separator strings like
// "Today's date is 2026-07/01." never match. This is what filters out
// user-authored prose like "Today is foo." or "His date is 2026-06-30." from
// being touched.
var (
	datelineRegexHyphen = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})-(\d{2})-(\d{2})\.`)
	datelineRegexSlash  = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})/(\d{2})/(\d{2})\.`)
)

// systemReminderRegex matches a <system-reminder> block. The dateline lives in
// this block once the conversation has advanced past the first turn (system
// prompt caching hides the top-level system block for subsequent turns), so
// the messages[].content[] scan is confined to what lives inside these tags.
var systemReminderRegex = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// DatelineHit records what a single rewrite normalized, for observability.
type DatelineHit struct {
	// ApostropheVariant is one of "ascii" (U+0027), "u2019", "u02bc", "u02b9".
	ApostropheVariant string
	// DateSeparator is either "-" or "/" as seen before normalization.
	DateSeparator string
}

// canonicalize returns the canonical form of a matched dateline sentence.
// The output always uses ASCII apostrophe and hyphen separators.
func canonicalize(year, month, day string) string {
	return fmt.Sprintf("Today's date is %s-%s-%s.", year, month, day)
}

func apostropheVariant(r rune) string {
	switch r {
	case '’':
		return "u2019"
	case 'ʼ':
		return "u02bc"
	case 'ʹ':
		return "u02b9"
	default:
		return "ascii"
	}
}

type datelineMatch struct {
	start, end       int
	apoRune          rune
	sep              string
	year, month, day string
}

func collectMatches(text string, re *regexp.Regexp, sep string) []datelineMatch {
	locs := re.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	out := make([]datelineMatch, 0, len(locs))
	for _, m := range locs {
		var apoRune rune
		for _, r := range text[m[2]:m[3]] {
			apoRune = r
			break
		}
		out = append(out, datelineMatch{
			start:   m[0],
			end:     m[1],
			apoRune: apoRune,
			sep:     sep,
			year:    text[m[4]:m[5]],
			month:   text[m[6]:m[7]],
			day:     text[m[8]:m[9]],
		})
	}
	return out
}

// NormalizeText canonicalizes standalone environment datelines outside fenced code.
// It returns the possibly-rewritten text and the list of
// hits observed. When no match is found the original string is returned
// verbatim (byte-identical), and the returned hit slice is nil.
func NormalizeText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, "date is ") {
		return text, nil
	}
	var out strings.Builder
	out.Grow(len(text))
	var hits []DatelineHit
	fence := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		marker := ""
		if strings.HasPrefix(trimmed, "```") {
			marker = "```"
		}
		if strings.HasPrefix(trimmed, "~~~") {
			marker = "~~~"
		}
		if marker != "" {
			switch fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}
			_, _ = out.WriteString(line)
			continue
		}
		if fence != "" {
			_, _ = out.WriteString(line)
			continue
		}
		matches := collectMatches(trimmed, datelineRegexHyphen, "-")
		matches = append(matches, collectMatches(trimmed, datelineRegexSlash, "/")...)
		if len(matches) != 1 || matches[0].start != 0 || matches[0].end != len(trimmed) {
			_, _ = out.WriteString(line)
			continue
		}
		match := matches[0]
		canonical := canonicalize(match.year, match.month, match.day)
		if canonical == trimmed {
			_, _ = out.WriteString(line)
			continue
		}
		start := strings.Index(line, trimmed)
		_, _ = out.WriteString(line[:start])
		_, _ = out.WriteString(canonical)
		_, _ = out.WriteString(line[start+len(trimmed):])
		hits = append(hits, DatelineHit{ApostropheVariant: apostropheVariant(match.apoRune), DateSeparator: match.sep})
	}
	if len(hits) == 0 {
		return text, nil
	}
	return out.String(), hits
}

// normalizeSystemReminderScopedText scans only the <system-reminder> blocks
// inside text and normalizes datelines inside them. Text outside the blocks is
// preserved byte-for-byte, so user prose, tool_result content, code blocks,
// or shell commands that happen to contain an apostrophe or a slash date are
// never touched.
func normalizeSystemReminderScopedText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, "<system-reminder>") {
		return text, nil
	}
	locs := systemReminderRegex.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	var hits []DatelineHit
	changed := false
	for _, loc := range locs {
		_, _ = b.WriteString(text[prev:loc[0]])
		block := text[loc[0]:loc[1]]
		inner := block[len("<system-reminder>") : len(block)-len("</system-reminder>")]
		normalizedInner, blockHits := NormalizeText(inner)
		normalized := "<system-reminder>" + normalizedInner + "</system-reminder>"
		if normalized != block {
			changed = true
		}
		_, _ = b.WriteString(normalized)
		hits = append(hits, blockHits...)
		prev = loc[1]
	}
	if !changed {
		return text, nil
	}
	_, _ = b.WriteString(text[prev:])
	return b.String(), hits
}

// NormalizeDateline scans an Anthropic /v1/messages request body and rewrites
// every fingerprinted dateline sentence back to its canonical ASCII form.
//
// Scope (mirroring where genuine clients place the sentence):
//   - `system` string, or `.text` field of each text-typed block in `system`.
//   - Text bodies inside `messages[i].content` — but ONLY the substrings that
//     appear inside `<system-reminder>...</system-reminder>` tags. Free user
//     prose, tool_use.input, tool_result.content, and other block types are
//     never scanned, guaranteeing that legitimate text like a code block, a
//     shell command, or a chat message that mentions today's date is never
//     accidentally rewritten.
//
// The function is a pure transform: it never modifies the input slice, and if
// no rewrite is needed it returns the original slice (identity), a nil hit
// slice, and changed=false.
func NormalizeDateline(body []byte) ([]byte, []DatelineHit, bool) {
	if len(body) == 0 {
		return body, nil, false
	}
	out := body
	var hits []DatelineHit
	changed := false

	sys := gjson.GetBytes(out, "system")
	if sys.Exists() {
		switch {
		case sys.Type == gjson.String:
			normalized, sysHits := NormalizeText(sys.String())
			if normalized != sys.String() {
				if next, err := sjson.SetBytes(out, "system", normalized); err == nil {
					out = next
					changed = true
					hits = append(hits, sysHits...)
				}
			}
		case sys.IsArray():
			idx := 0
			sys.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "text" {
					t := item.Get("text")
					if t.Exists() && t.Type == gjson.String {
						normalized, textHits := NormalizeText(t.String())
						if normalized != t.String() {
							path := fmt.Sprintf("system.%d.text", idx)
							if next, err := sjson.SetBytes(out, path, normalized); err == nil {
								out = next
								changed = true
								hits = append(hits, textHits...)
							}
						}
					}
				}
				idx++
				return true
			})
		}
	}

	messages := gjson.GetBytes(out, "messages")
	if messages.IsArray() {
		msgIdx := -1
		messages.ForEach(func(_, msg gjson.Result) bool {
			msgIdx++
			content := msg.Get("content")
			if !content.Exists() {
				return true
			}
			switch {
			case content.Type == gjson.String:
				normalized, contentHits := normalizeSystemReminderScopedText(content.String())
				if normalized != content.String() {
					path := fmt.Sprintf("messages.%d.content", msgIdx)
					if next, err := sjson.SetBytes(out, path, normalized); err == nil {
						out = next
						changed = true
						hits = append(hits, contentHits...)
					}
				}
			case content.IsArray():
				contentIdx := -1
				content.ForEach(func(_, block gjson.Result) bool {
					contentIdx++
					if block.Get("type").String() != "text" {
						return true
					}
					t := block.Get("text")
					if !t.Exists() || t.Type != gjson.String {
						return true
					}
					normalized, textHits := normalizeSystemReminderScopedText(t.String())
					if normalized != t.String() {
						path := fmt.Sprintf("messages.%d.content.%d.text", msgIdx, contentIdx)
						if next, err := sjson.SetBytes(out, path, normalized); err == nil {
							out = next
							changed = true
							hits = append(hits, textHits...)
						}
					}
					return true
				})
			}
			return true
		})
	}

	if !changed {
		return body, nil, false
	}
	return out, hits, true
}
