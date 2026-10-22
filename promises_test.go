package main

// The promises on the front of the README, tested by reading the source.
//
// Every one of these could be broken by a change that compiles and passes
// everything else, and the person making it would have no reason to think
// twice. Reading the source in a test is the only way the promise and the
// program stay the same thing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sources is every .go file that ships, which is all of them except the
// tests. A test writes files by definition and that is fine.
func sources(t *testing.T) map[string]string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(body)
	}
	if len(out) < 8 {
		t.Fatalf("only found %d source files, so this test is not looking at the program", len(out))
	}
	return out
}

// "It only reads." A repository is somebody's work and an auditor that
// writes to it is a different kind of program.
func TestItNeverWrites(t *testing.T) {
	forbidden := []string{
		"os.WriteFile", "os.Create", "os.Remove", "os.RemoveAll", "os.Rename",
		"os.Mkdir", "os.MkdirAll", "os.MkdirTemp", "os.Truncate", "os.Chmod",
		"os.OpenFile", "os.Symlink", "os.Link",
	}
	for name, body := range sources(t) {
		for _, call := range forbidden {
			if strings.Contains(body, call) {
				t.Errorf("%s calls %s, and this program does not write", name, call)
			}
		}
	}
}

// "No socket is opened." Nothing here goes anywhere, so there is no
// version check, no telemetry and no lookup of a token to see if it works.
func TestItNeverOpensASocket(t *testing.T) {
	forbidden := []string{`"net"`, `"net/http"`, `"net/url"`, `"net/smtp"`, `"crypto/tls"`}
	for name, body := range sources(t) {
		for _, imported := range forbidden {
			if strings.Contains(body, imported) {
				t.Errorf("%s imports %s, and nothing here goes anywhere", name, imported)
			}
		}
	}
}

// "It never runs git." The whole object store is read by hand, which is
// the reason it can see the objects git will not show without being asked
// exactly the right way.
func TestItNeverRunsAnything(t *testing.T) {
	for name, body := range sources(t) {
		if strings.Contains(body, "os/exec") || strings.Contains(body, "exec.Command") {
			t.Errorf("%s runs another program", name)
		}
	}
}

// Nothing outside the standard library. It is one binary and it stays one
// binary.
func TestNoDependencies(t *testing.T) {
	body, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "require") {
		t.Errorf("go.mod has a dependency in it:\n%s", body)
	}
}

// A rule without a capturing group would match the secret and cover
// nothing, and the covering is the whole promise.
func TestEveryRuleCoversExactlyOneThing(t *testing.T) {
	for _, d := range detectors {
		if n := d.Match.NumSubexp(); n != 1 {
			t.Errorf("%s has %d capturing groups and needs exactly one, "+
				"around the part that must never be printed", d.Name, n)
		}
		if strings.TrimSpace(d.Name) == "" {
			t.Error("a rule with no name would print as an empty finding")
		}
	}
}

// The covering happens when the blob comes off the disk, so there is no
// later point at which the original could be printed by mistake.
func TestCoveringTakesTheSecretOut(t *testing.T) {
	live := "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	body := "STRIPE_KEY=" + live + "\nAWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY\n"

	hits := detect(body)
	if len(hits) < 2 {
		t.Fatalf("expected both to be found, got %d", len(hits))
	}
	safe := cover(body, hits)
	for _, secret := range []string{live, "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"} {
		if strings.Contains(safe, secret) {
			t.Errorf("%q survived the covering", secret)
		}
	}
	if !strings.Contains(safe, "STRIPE_KEY=") {
		t.Error("the covering took the name of the thing as well as the value")
	}
	if strings.Count(safe, covered) != 2 {
		t.Errorf("expected two markers, got %d in %q", strings.Count(safe, covered), safe)
	}
}

// Two rules matching the same run of text is the normal case, not the odd
// one, and the second one must not tear a hole in the first.
func TestOverlappingRulesStillCover(t *testing.T) {
	live := "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	body := "secret_key = \"" + live + "\""
	safe := cover(body, detect(body))
	if strings.Contains(safe, live) {
		t.Errorf("overlapping rules left the secret in: %q", safe)
	}
}

// Nagging somebody for doing it right is the fastest way to get a tool
// uninstalled, and all four of these are what the report is asking for.
func TestDoingItProperlyIsNotAFinding(t *testing.T) {
	for _, line := range []string{
		"TOKEN = os.environ['TOKEN']",
		"password = getpass.getpass()",
		"key = os.getenv(\"API_KEY\")",
		"api_key = \"your-api-key-here\"",
		"SECRET_KEY=xxxxxxxxxxxx",
		"TOKEN=${GITHUB_TOKEN}",
	} {
		if hits := detect(line); len(hits) > 0 {
			t.Errorf("%q was reported as a secret", line)
		}
	}
}

// What a login form tells the browser to fill in is not a password. These
// are the shapes it takes in a page, the first of them word for word from
// the form that turned up eleven times in a real repository's history.
func TestFormWordsAreNotSecrets(t *testing.T) {
	for _, line := range []string{
		`  entrada.autocomplete = modoRegistro ? "new-password" : "current-password";`,
		`passwordAutocomplete: "current-password",`,
		`const passwordHint = 'new-password'`,
		`<input name="code" autocomplete="one-time-code">`,
	} {
		if hits := detect(line); len(hits) > 0 {
			t.Errorf("%q was reported as a secret", line)
		}
	}
	// And a password that happens to start the same way still is one.
	if hits := detect(`password = "current-password-2026!"`); len(hits) == 0 {
		t.Error("a real password beginning with the word was let through")
	}
}

// The folder the repository sits in is this machine's, and the user name is
// in it. v1 printed it whole in the heading.
func TestTheHomeFolderIsNotShown(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder here")
	}
	dir := filepath.Join(home, "projects", "site")
	if got := where(dir, false); got != "~/projects/site" {
		t.Errorf("shown as %s", got)
	}
	if got := where(dir, true); got != dir {
		t.Errorf("--reveal showed %s", got)
	}
	if got := where(".", false); got != "." && !strings.HasPrefix(got, "~") {
		t.Errorf("the current folder came out as %s", got)
	}
	_, err = os.ReadFile(filepath.Join(home, "no-such-file-for-airing"))
	if got := plain(err); strings.Contains(got, home) {
		t.Errorf("an error still carries the home folder: %s", got)
	}
}

// The report has to say what it could not read. A tool that quietly skips
// the part it could not open and prints a clean bill is worse than no
// tool.
func TestGapsAreReported(t *testing.T) {
	b := leaky(t)
	a := inspect(b.open())
	a.Gaps = append(a.Gaps, "objects/ab/cdef: unreadable")

	text := renderReport(a.writeUp(false))
	if !strings.Contains(text, "could not be read") {
		t.Error("something unreadable did not reach the report")
	}
	if !strings.Contains(text, "objects/ab/cdef") {
		t.Error("the report did not say which part it could not read")
	}
}

// Every flag has to be in the README, or the README stops being what the
// program does and becomes what it did once.
func TestReadmeMatchesTheFlags(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Skip("no README yet")
	}
	readme := string(body)
	for _, flag := range []string{"--reveal", "--version"} {
		if !strings.Contains(readme, flag) {
			t.Errorf("%s is a flag and the README does not mention it", flag)
		}
	}
	for _, promise := range []string{"never", "reads"} {
		if !strings.Contains(strings.ToLower(readme), promise) {
			t.Errorf("the README no longer says anything about %q", promise)
		}
	}
}

// The two piles are the whole idea. A finding in the wrong one either
// frightens somebody about something that is not happening or waves
// through something that is.
func TestTheTwoPilesAreNeverMixed(t *testing.T) {
	text := renderOf(t, leaky(t), false)
	pile := map[string]string{
		"goes out anyway":       "PUBLIC ",
		"must not be published": "PUBLIC ",
		"on no branch":          "HANDED ",
		"the reflog remembers":  "HANDED ",
	}
	for phrase, want := range pile {
		at := strings.Index(text, phrase)
		if at < 0 {
			t.Errorf("the report never says %q", phrase)
			continue
		}
		if got := lastTagBefore(text, at); got != want {
			t.Errorf("%q came out as %q and belongs in %q", phrase, got, want)
		}
	}
}
