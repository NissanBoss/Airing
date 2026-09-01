package main

// The small patterns: what counts as internal, and what a gitignore rule
// matches.

import (
	"path"
	"regexp"
	"strings"
)

var (
	// A hostname nothing outside can resolve, which in a public repository
	// is a description of the network on the other side.
	insideHost = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+(?:internal|intranet|corp|lan|local|localdomain)\b`)

	// The same idea pointed at the contents of a file rather than at a
	// commit message, and stricter on purpose. The pattern above matches
	// this.internal and config.corp, which are property accesses in half
	// the JavaScript ever written, so inside a file the name has to sit
	// somewhere a host actually goes: after a scheme, after the at sign of
	// a URL, on the right of an assignment or a colon, or alone inside
	// quotes. The
	// .local suffix is left out here as well, because a made up .local name
	// in a development config is ordinary, and saying so every time is how
	// a report gets skimmed instead of read.
	insideFile = regexp.MustCompile(`(?i)(?:://|@|=|:[ \t]+|["'` + "`" + `])((?:[a-z0-9-]+\.)+(?:internal|intranet|corp|lan|localdomain))(?:[:/"'` + "`" + `,;)\]}]|\s|$)`)

	// A ticket, which is a tracker somebody can go and look for, and often
	// the name of the customer the work was for.
	ticket = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d{1,6}\b`)

	// The words that turn up in a branch name when it is about something
	// that was not meant to be public.
	internalWord = regexp.MustCompile(`(?i)\b(?:[A-Z][A-Z0-9]{1,9}-\d{1,6}|internal|confidential|customer|client|acquisition|layoff|nda|prod-fix|hotfix-[a-z0-9]+)\b`)

	// A remote or submodule address that is not a public host.
	publicHost = regexp.MustCompile(`(?i)\b(?:github\.com|gitlab\.com|bitbucket\.org|codeberg\.org|sr\.ht|git\.sr\.ht|sourceforge\.net)\b`)
)

// Plenty of things have the shape of a ticket and are not one. "fix UTF-8
// handling" and "move to TLS-1" both match the pattern, and a report that
// calls those internal references is a report that gets skimmed. The list
// is standards and algorithms, which is where the collisions all are.
var notATracker = map[string]bool{
	"AES": true, "ANSI": true, "ARM": true, "ASCII": true, "BASE": true,
	"CRC": true, "CVE": true, "CVSS": true, "EC": true, "ECMA": true,
	"GPT": true, "HTTP": true, "IEEE": true, "ISO": true, "MD": true,
	"NIST": true, "P": true, "PEP": true, "RFC": true, "RSA": true,
	"SHA": true, "SSL": true, "TLS": true, "UTC": true, "UTF": true,
	"X": true,
}

// isTicket says whether something with the shape of a ticket is worth
// reporting as one.
func isTicket(s string) bool {
	prefix, _, found := strings.Cut(s, "-")
	return found && !notATracker[strings.ToUpper(prefix)]
}

// privateLooking says whether an address points somewhere the public
// cannot follow. Anything that is not one of the well known forges is taken
// as private, which errs towards saying so, and that is the right way round
// for a report about what is about to be published.
func privateLooking(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	if publicHost.MatchString(url) {
		return false
	}
	return strings.Contains(url, "@") || strings.Contains(url, "://") ||
		strings.Contains(url, ":")
}

// matchesIgnore is enough of the gitignore rules to answer the one question
// asked of it: does this tracked path match a rule that was supposed to
// keep it out.
//
// Deliberately not the whole specification. Negations, the difference a
// leading slash makes to where a rule anchors, and the double star are all
// left out, because a wrong yes here only ever produces one quiet note and
// implementing the whole thing correctly is a week nobody asked for.
func matchesIgnore(rule, where string) bool {
	rule = strings.TrimSuffix(strings.TrimPrefix(rule, "/"), "/")
	if rule == "" {
		return false
	}
	if rule == where || strings.HasPrefix(where, rule+"/") {
		return true
	}
	base := path.Base(where)
	if rule == base {
		return true
	}
	if matched, err := path.Match(rule, base); err == nil && matched {
		return true
	}
	for _, segment := range strings.Split(path.Dir(where), "/") {
		if segment == rule {
			return true
		}
	}
	return false
}
