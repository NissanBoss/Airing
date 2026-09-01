package main

// The files that should not be in a repository anybody can read.
//
// The list is the program, and here it runs over the whole history rather
// than the working tree, which is the difference that matters. Every one of
// these has been deleted from somebody's repository at some point, and
// deleting it from the tree does not take it off the branch: the commit
// that added it is still there and still goes out.

import (
	"path"
	"strings"
)

type Named struct {
	Pattern string
	What    string
	// Why is what somebody who fetches the repository gets. A name on the
	// list without one is a finding nobody acts on.
	Why string
	// Grave marks the ones that are a credential rather than a mess.
	Grave bool
	// NeedsKey means the name is not enough and the blob has to actually
	// hold a private key header, because most .pem files are certificates
	// and those are public by design.
	NeedsKey bool
}

func named() []Named {
	return []Named{
		{Pattern: ".env", What: "an environment file", Grave: true,
			Why: "the file the application reads its secrets out of"},
		{Pattern: ".env.local", What: "an environment file", Grave: true,
			Why: "usually the one with the real values in it"},
		{Pattern: ".env.production", What: "an environment file", Grave: true,
			Why: "the production secrets"},
		{Pattern: ".env.development", What: "an environment file", Grave: true,
			Why: "development secrets, which are usually the same secrets"},
		{Pattern: "id_rsa", What: "a private ssh key", Grave: true,
			Why: "whoever clones it can log in wherever that key is trusted"},
		{Pattern: "id_ed25519", What: "a private ssh key", Grave: true,
			Why: "whoever clones it can log in wherever that key is trusted"},
		{Pattern: "id_dsa", What: "a private ssh key", Grave: true,
			Why: "whoever clones it can log in wherever that key is trusted"},
		{Pattern: "*.pem", What: "a private key", Grave: true, NeedsKey: true,
			Why: "holding the private half is being the server"},
		{Pattern: "*.key", What: "a private key", Grave: true, NeedsKey: true,
			Why: "holding the private half is being the server"},
		{Pattern: "*.p12", What: "a key store", Grave: true,
			Why: "a bundle with the private key in it, behind a password nobody changed"},
		{Pattern: "*.pfx", What: "a key store", Grave: true,
			Why: "a bundle with the private key in it, behind a password nobody changed"},
		{Pattern: "*.jks", What: "a Java key store", Grave: true,
			Why: "the default password is changeit and it usually still is"},
		{Pattern: ".npmrc", What: "an npm configuration", Grave: true,
			Why: "if it has an auth token in it, whoever holds it can publish under your name"},
		{Pattern: ".netrc", What: "a netrc", Grave: true,
			Why: "logins for whatever machines it lists"},
		{Pattern: ".pypirc", What: "a PyPI configuration", Grave: true,
			Why: "uploads packages under your name"},
		{Pattern: ".git-credentials", What: "stored git logins", Grave: true,
			Why: "a list of URLs with the password written into each one"},
		{Pattern: ".htpasswd", What: "a password file", Grave: true,
			Why: "hashes to work on offline, at whatever speed the attacker likes"},
		{Pattern: "credentials.json", What: "a credentials file", Grave: true,
			Why: "usually a service account, which is an identity rather than a password"},
		{Pattern: "service-account.json", What: "a Google service account", Grave: true,
			Why: "a key that does not expire, for an account that usually has too much"},
		{Pattern: "kubeconfig", What: "a Kubernetes configuration", Grave: true,
			Why: "reaches the clusters it lists, as whoever it says you are"},
		{Pattern: "terraform.tfstate", What: "Terraform state", Grave: true,
			Why: "state files keep every secret the plan touched in plain text, " +
				"which surprises people who were careful with the variables"},
		{Pattern: "*.tfvars", What: "Terraform variables", Grave: true,
			Why: "the values that were fed to the plan"},
		{Pattern: "secrets.yml", What: "a secrets file", Grave: true,
			Why: "named for what is in it"},
		{Pattern: "secrets.yaml", What: "a secrets file", Grave: true,
			Why: "named for what is in it"},
		{Pattern: "secrets.json", What: "a secrets file", Grave: true,
			Why: "named for what is in it"},
		{Pattern: "*.pdb", What: "debug symbols",
			Why: "the absolute build path, which names an account, and every function name"},
		{Pattern: "*.sql", What: "a database dump or schema",
			Why: "the shape of the data, and if it is a dump then the data"},
		{Pattern: "*.sqlite", What: "a database", Why: "the data itself"},
		{Pattern: "*.db", What: "a database", Why: "the data itself"},
		{Pattern: "*.bak", What: "a backup file",
			Why: "the version before somebody fixed something"},
		{Pattern: "*.orig", What: "a merge leftover",
			Why: "the version before a conflict was resolved"},
		{Pattern: "*.log", What: "a log file",
			Why: "whatever was running printed, which is usually paths and sometimes tokens"},
		{Pattern: ".DS_Store", What: "a macOS folder record",
			Why: "it lists what was in the folder when it was committed, including " +
				"files that were never committed"},
		{Pattern: "Thumbs.db", What: "a Windows thumbnail cache",
			Why: "thumbnails of images that were in the folder, including removed ones"},
		{Pattern: "*.pcap", What: "a packet capture",
			Why: "whatever was on the wire, credentials in the clear included"},
		{Pattern: "*.har", What: "a browser session recording",
			Why: "every request and response, with the cookies and the auth headers in them"},
	}
}

// Built once. This runs over every path in the history, which in a real
// repository is tens of thousands of them.
var everything = named()

func carries(where string) (Named, bool) {
	base := path.Base(where)
	for _, n := range everything {
		if strings.HasPrefix(n.Pattern, "*.") {
			if strings.HasSuffix(strings.ToLower(base), n.Pattern[1:]) {
				return n, true
			}
			continue
		}
		if base == n.Pattern {
			return n, true
		}
	}
	return Named{}, false
}

// A path whose name says it is an example is an example, and a history is
// full of them.
func isTemplate(where string) bool {
	base := strings.ToLower(path.Base(where))
	for _, ending := range []string{".example", ".sample", ".template", ".dist", ".default"} {
		if strings.HasSuffix(base, ending) {
			return true
		}
	}
	if strings.Contains(base, ".example.") || strings.Contains(base, ".sample.") {
		return true
	}
	lowered := strings.ToLower(where)
	for _, segment := range []string{"/testdata/", "/fixtures/", "/test/", "/tests/",
		"/examples/", "/example/", "/spec/", "/__mocks__/", "/vendor/", "/node_modules/"} {
		if strings.Contains("/"+lowered, segment) {
			return true
		}
	}
	return false
}

// looksLikeATest says whether a file is a test, by the naming convention
// of whatever language it is in.
//
// Not the same question as isTemplate, and it does not have the same
// answer. A template is skipped. A test is reported, and reported in one
// group rather than one finding each: a repository of any size has
// fixtures with made up tokens in them, and calling each of those a
// credential to rotate is how a report stops being read. The ones that are
// not made up are the worst kind there is, because a test that talks to
// something real needs a credential that works and nobody opens a test
// file before publishing.
func looksLikeATest(where string) bool {
	base := path.Base(where)
	// These two go by their own capitals, because lowercasing them first
	// makes latest.java end in test.java.
	if strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.cs") {
		return true
	}
	lowered := strings.ToLower(base)
	for _, ending := range []string{
		"_test.go", "_test.py", "_test.rb", "_test.rs", "_test.exs",
		"_test.js", "_test.ts", "_spec.rb",
		".test.js", ".test.ts", ".test.jsx", ".test.tsx",
		".spec.js", ".spec.ts", ".spec.jsx", ".spec.tsx",
	} {
		if strings.HasSuffix(lowered, ending) {
			return true
		}
	}
	return strings.HasPrefix(lowered, "test_") || strings.HasPrefix(lowered, "conftest.")
}
