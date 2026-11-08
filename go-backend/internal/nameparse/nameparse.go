// Package nameparse ports the person-name parsing that the pinned upstream uses
// to derive an author's "Last, First" form (server/utils/parsers/parseNameString.js
// nameToLastFirst over server/utils/parsers/parseFullName.js, which is the
// parse-full-name package). It is a literal port, quirks included, so the
// stored lastFirst column matches what upstream writes.
package nameparse

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// parsed is parseFullName's result object.
type parsed struct {
	title, first, middle, last, nick, suffix string
}

var (
	conjunctions = []string{"&", "and", "et", "e", "of", "the", "und", "y"}
	suffixes     = []string{"esq", "esquire", "jr", "jnr", "sr", "snr", "2", "ii", "iii", "iv",
		"md", "phd", "j.d.", "ll.m.", "m.d.", "d.o.", "d.c.", "p.c.", "ph.d."}
	prefixes = []string{"ab", "bar", "bin", "da", "dal", "de", "de la", "del", "della", "der",
		"di", "du", "ibn", "l'", "la", "le", "san", "st", "st.", "ste", "ter", "van",
		"van de", "van der", "van den", "vel", "ver", "vere", "von"}
	titles = []string{"dr", "miss", "mr", "mrs", "ms", "prof", "sir", "frau", "herr", "hr",
		"monsieur", "captain", "doctor", "judge", "officer", "professor", "ind", "misc", "mx"}
	forceCase = []string{"e", "y", "av", "af", "da", "dal", "de", "del", "der", "di",
		"la", "le", "van", "der", "den", "vel", "von", "II", "III", "IV", "J.D.", "LL.M.",
		"M.D.", "D.O.", "D.C.", "Ph.D."}

	// JavaScript's \s covers the Unicode space separators, RE2's does not.
	space = `[\s\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`
	// The nickname expression of parseFullName.js (single quotes, double
	// quotes, square brackets, parentheses, optional trailing comma).
	nicknameRE = regexp.MustCompile(space + `(?:[‘’']([^‘’']+)[‘’']|[“”"]([^“”"]+)[“”"]|\[([^\]]+)\]|\(([^\)]+)\)),?` + space)
)

func indexOf(list []string, v string) int {
	for i, e := range list {
		if e == v {
			return i
		}
	}
	return -1
}

func contains(list []string, v string) bool { return indexOf(list, v) > -1 }

// splice removes count elements at index and returns the removed ones and the
// shortened slice, with Array.prototype.splice's clamping.
func splice(s []string, index, count int) ([]string, []string) {
	if index > len(s) {
		index = len(s)
	}
	if index < 0 {
		index = 0
	}
	end := index + count
	if end > len(s) {
		end = len(s)
	}
	removed := append([]string(nil), s[index:end]...)
	out := append(append([]string(nil), s[:index]...), s[end:]...)
	return removed, out
}

func checkPart(part string) string {
	if strings.HasSuffix(part, ".") {
		return strings.ToLower(part[:len(part)-1])
	}
	return strings.ToLower(part)
}

func trimSpace(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// parseFullName is parse-full-name's default call (short lists, auto case fix).
func parseFullName(name string) parsed {
	var out parsed
	if name == "" {
		return out
	}
	name = trimSpace(name)
	fixCase := name == strings.ToUpper(name) || name == strings.ToLower(name)
	finish := func() parsed {
		if fixCase {
			out = fixParsedNameCase(out)
		}
		return out
	}

	padded := " " + name + " "
	matches := nicknameRE.FindAllString(padded, -1)
	if len(matches) == 1 {
		out.nick = strings.TrimSuffix(trimEnds(matches[0]), ",")
		name = trimSpace(strings.Replace(padded, matches[0], " ", 1))
	} else if len(matches) > 1 {
		nicks := make([]string, 0, len(matches))
		for _, m := range matches {
			name = trimSpace(strings.Replace(" "+name+" ", m, " ", 1))
			nicks = append(nicks, strings.TrimSuffix(trimEnds(m), ","))
		}
		out.nick = strings.Join(nicks, ", ")
	}
	if trimSpace(name) == "" {
		return finish()
	}

	var parts []string
	commas := []string{""}
	for _, part := range strings.Split(name, " ") {
		comma := ""
		if strings.HasSuffix(part, ",") {
			comma = ","
			part = part[:len(part)-1]
		}
		parts = append(parts, part)
		commas = append(commas, comma)
	}

	var found []string
	// take removes the parts (from the end down to index from) that appear in
	// list, keeping the comma bookkeeping of the suffix and title loops.
	take := func(list []string, from int) {
		for i := len(parts) - 1; i >= from; i-- {
			check := checkPart(parts[i])
			if contains(list, check) || contains(list, check+".") {
				var removed []string
				removed, parts = splice(parts, i, 1)
				found = append(removed, found...)
				if i < len(commas) && commas[i] == "," {
					_, commas = splice(commas, i+1, 1)
				} else {
					_, commas = splice(commas, i, 1)
				}
			}
		}
	}

	// Suffix: matching parts after the first.
	take(suffixes, 1)
	switch {
	case len(found) == 1:
		out.suffix = found[0]
	case len(found) > 1:
		out.suffix = strings.Join(found, ", ")
	}
	found = nil
	if len(parts) == 0 {
		return finish()
	}

	// Title: matching parts anywhere.
	take(titles, 0)
	switch {
	case len(found) == 1:
		out.title = found[0]
	case len(found) > 1:
		out.title = strings.Join(found, ", ")
	}
	found = nil
	if len(parts) == 0 {
		return finish()
	}

	if len(parts) > 1 {
		for i := len(parts) - 2; i >= 0; i-- {
			if contains(prefixes, strings.ToLower(parts[i])) {
				parts[i] = parts[i] + " " + parts[i+1]
				_, parts = splice(parts, i+1, 1)
				_, commas = splice(commas, i+1, 1)
			}
		}
	}

	if len(parts) > 2 {
		for i := len(parts) - 3; i >= 0; i-- {
			if contains(conjunctions, strings.ToLower(parts[i+1])) {
				parts[i] = parts[i] + " " + parts[i+1] + " " + parts[i+2]
				_, parts = splice(parts, i+1, 2)
				_, commas = splice(commas, i+1, 2)
				i--
			}
		}
	}

	// Suffix: items after extra commas.
	if len(commas) > 0 {
		commas = commas[:len(commas)-1]
	}
	firstComma := indexOf(commas, ",")
	remaining := 0
	for _, c := range commas {
		if c != "" {
			remaining++
		}
	}
	if firstComma > 1 || remaining > 1 {
		for i := len(parts) - 1; i >= 2; i-- {
			if i < len(commas) && commas[i] == "," {
				var removed []string
				removed, parts = splice(parts, i, 1)
				found = append(removed, found...)
				_, commas = splice(commas, i, 1)
				remaining--
			} else {
				break
			}
		}
	}
	if len(found) > 0 {
		if out.suffix != "" {
			found = append([]string{out.suffix}, found...)
		}
		out.suffix = strings.Join(found, ", ")
	}

	if remaining > 0 {
		// `if (nameCommas.indexOf(','))`: skipped only when the first comma is at 0.
		if idx := indexOf(commas, ","); idx != 0 {
			var removed []string
			removed, parts = splice(parts, 0, idx)
			out.last = strings.Join(removed, " ")
			_, commas = splice(commas, 0, idx)
		}
	} else {
		out.last = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return finish()
	}

	// First name, then everything left is the middle name.
	out.first = parts[0]
	parts = parts[1:]
	if len(parts) == 0 {
		return finish()
	}
	out.middle = strings.Join(parts, " ")
	return finish()
}

// trimEnds is `match.slice(2).slice(0, -2)`: the leading space and quote and
// the trailing two characters.
func trimEnds(m string) string {
	r := []rune(m)
	if len(r) < 4 {
		return ""
	}
	return string(r[2 : len(r)-2])
}

// fixParsedNameCase is parseFullName.js's fixParsedNameCase with fixCase on.
// Its suffix branch indexes the label list with the word index, which only
// reaches 'suffix' for the sixth word, and tests `!suffixList.indexOf(word)`,
// which is true only for suffixList[0]; both quirks are kept.
func fixParsedNameCase(p parsed) parsed {
	fields := []*string{&p.title, &p.first, &p.middle, &p.last, &p.nick, &p.suffix}
	labels := []string{"title", "first", "middle", "last", "nick", "suffix"}
	for _, field := range fields {
		if *field == "" {
			continue
		}
		words := strings.Split(*field, " ")
		for j, word := range words {
			lower := strings.ToLower(word)
			forced := -1
			for k, f := range forceCase {
				if strings.ToLower(f) == lower {
					forced = k
					break
				}
			}
			switch {
			case forced > -1:
				words[j] = forceCase[forced]
			case utf8.RuneCountInString(word) == 1:
				words[j] = strings.ToUpper(word)
			case isMcCase(word):
				r := []rune(word)
				words[j] = string(r[:3]) + strings.ToLower(string(r[3:]))
			case j < len(labels) && labels[j] == "suffix" && !strings.HasSuffix(word, ".") && indexOf(suffixes, lower) == 0:
				if word == lower {
					words[j] = strings.ToUpper(word)
				}
			default:
				r := []rune(word)
				if len(r) > 0 {
					words[j] = strings.ToUpper(string(r[:1])) + strings.ToLower(string(r[1:]))
				}
			}
		}
		*field = strings.Join(words, " ")
	}
	return p
}

func isMcCase(word string) bool {
	r := []rune(word)
	if len(r) <= 2 {
		return false
	}
	first, second, rest := string(r[:1]), string(r[1:2]), string(r[2:])
	return first == strings.ToUpper(first) && second == strings.ToLower(second) && rest == strings.ToUpper(rest)
}

// ToLastFirst is parseNameString.nameToLastFirst: "Last, First Middle", or
// whichever of the two is present.
func ToLastFirst(name string) string {
	p := parseFullName(name)
	first := p.first
	if first != "" && p.middle != "" {
		first += " " + p.middle
	}
	switch {
	case p.last == "":
		return first
	case first == "":
		return p.last
	}
	return p.last + ", " + first
}
