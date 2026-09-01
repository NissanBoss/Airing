package main

// Finding the credentials in the history.
//
// The half that matters here is not how a key is found but when it was put
// there. A key in the working tree is one somebody can see. A key in a
// commit from two years ago that was taken out the following week is one
// nobody has looked at since, and it goes out with the push exactly the
// same, because the commit that had it is still on the branch.

import (
	"regexp"
	"sort"
	"strings"
)

// Sort is what kind of rule found it. Not called Kind, because that is what
// a git object has and one word for two things in the same package is how
// somebody reads the wrong one at two in the morning.
type Sort int

const (
	// Shaped is a token that gives itself away whatever it is called.
	Shaped Sort = iota
	// Labelled is a value still sitting next to the name of what it is.
	Labelled
	// Account is an identifier rather than a secret.
	Account
)

type Detector struct {
	Name string
	Sort Sort
	// Match must have exactly one capturing group, around the part that
	// must never reach the screen. A test refuses to let a rule exist
	// without one.
	Match *regexp.Regexp
	Note  string
}

// covered is what a secret looks like once it is taken out. Fixed width, so
// the length of the thing is not on the screen either.
const covered = "<taken-out>"

var detectors = []Detector{
	{Name: "a Stripe live key", Sort: Shaped,
		Match: regexp.MustCompile(`\b((?:sk|rk)_live_[A-Za-z0-9]{16,})\b`),
		Note: "The publishable key and the secret key look alike and sit next to " +
			"each other in the dashboard, which is how the secret one ends up in " +
			"the file somebody meant to put the other one in."},
	{Name: "a GitHub token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,255})\b`)},
	{Name: "a GitHub fine grained token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(github_pat_[A-Za-z0-9_]{60,255})\b`)},
	{Name: "a GitLab token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(glpat-[A-Za-z0-9_-]{20,})\b`)},
	{Name: "a Slack token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(xox[baprse]-[A-Za-z0-9-]{10,})\b`)},
	{Name: "an OpenAI or Anthropic key", Sort: Shaped,
		Match: regexp.MustCompile(`\b(sk-(?:ant-)?[A-Za-z0-9_-]{20,})\b`)},
	{Name: "a Google API key", Sort: Shaped,
		Match: regexp.MustCompile(`\b(AIza[0-9A-Za-z_-]{35})\b`),
		Note: "Often deliberate for a maps key and often not restricted to a " +
			"referrer, which is the part people forget."},
	{Name: "an npm token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(npm_[A-Za-z0-9]{36})\b`)},
	{Name: "a SendGrid key", Sort: Shaped,
		Match: regexp.MustCompile(`\b(SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,})\b`)},
	{Name: "a Twilio key", Sort: Shaped,
		Match: regexp.MustCompile(`\b(SK[0-9a-fA-F]{32})\b`)},
	{Name: "a DigitalOcean token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(dop_v1_[a-f0-9]{64})\b`)},
	{Name: "a Hugging Face token", Sort: Shaped,
		Match: regexp.MustCompile(`\b(hf_[A-Za-z0-9]{30,})\b`)},
	{Name: "a Supabase or Firebase service key", Sort: Shaped,
		Match: regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{40,}\.[A-Za-z0-9_-]{20,})\b`),
		Note: "A signed token. The anon key and the service role key look the " +
			"same from outside and only one of them is meant to be shared."},
	{Name: "a private key", Sort: Shaped,
		Match: regexp.MustCompile(`(-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP )?PRIVATE KEY-----)`)},

	{Name: "a password in a connection string", Sort: Labelled,
		Match: regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?|mssql)://[^:/\s@"']+:([^@/\s"']{3,})@`),
		Note: "A database URL that made it into something a browser fetches. The " +
			"host in it is usually reachable from nowhere useful, and the " +
			"password is usually reused."},
	{Name: "a password or token set in a file", Sort: Labelled,
		Match: regexp.MustCompile(`(?im)^[^#;/]{0,80}?[A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)["']?\s*[:=]\s*("[^"]{6,}"|'[^']{6,}'|[^\s"',]{8,})`),
		Note: "The ordinary case: hardcoded to get something working, taken out " +
			"three commits later, and still on the branch."},

	{Name: "an AWS account key id", Sort: Account,
		Match: regexp.MustCompile(`\b((?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[0-9A-Z]{12,20})\b`),
		Note:  "Not the secret. It does name the account."},
}

// The values that are not secrets however much they look like one. Every
// repository has example configuration and test fixtures in it, and a
// history is every version of all of them.
var placeholder = regexp.MustCompile(`(?i)^["']?(` +
	`change[_-]?me|changeit|password|passwd|secret|token|(?:your|my|the|some)[\w.-]*|` +
	`example|examples|sample|test|testing|dummy|placeholder|redacted|` +
	`xxx+|yyy+|zzz+|aaa+|1234\d*|abc123|foo|bar|baz|qwerty|admin|root|` +
	`none|null|nil|true|false|undefined|empty|unset|todo|tbd|` +
	`\$\{?\w*\}?|<[^>]*>|%\w+%|\*+|\.\.\.|-+|process\.env\.\w+|import\.meta\.\w+` +
	`)["']?$`)

var reference = regexp.MustCompile(`^["']?(?:/|\./|\.\./|~/|[A-Za-z]:\\|\$|%|@|-)`)

// And the ways somebody fetches a secret properly, which is the opposite of
// a finding. Nagging people for writing os.environ['TOKEN'] would be
// telling them off for doing the one thing this tool is asking for.
var fetchedProperly = regexp.MustCompile(`(?i)^["']?(?:os\.environ|os\.getenv|getpass|dotenv|load_dotenv|userdata\.get|config\(|Secret|keyring\.|boto3\.|st\.secrets)`)

// Hit is one rule matching, and where.
type Hit struct {
	Rule int
	From int
	To   int
}

func detect(body string) []Hit {
	var hits []Hit
	for i, d := range detectors {
		for _, at := range d.Match.FindAllStringSubmatchIndex(body, -1) {
			if len(at) < 4 || at[2] < 0 {
				continue
			}
			if d.Sort != Shaped && notReallyASecret(strings.TrimSpace(body[at[2]:at[3]])) {
				continue
			}
			hits = append(hits, Hit{Rule: i, From: at[2], To: at[3]})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].From < hits[j].From })
	return hits
}

// notReallyASecret filters the values plainly standing in for one. The
// shaped tokens skip it: sk_live_ is never a placeholder, and somebody who
// put a real looking one in an example has a different problem.
func notReallyASecret(value string) bool {
	// Backticks come off along with the quotes. A readme saying
	// API_TOKEN=`xxxxxxxx` was reported as a real one, because the trailing
	// backtick stayed on the value and the list of things that are not
	// secrets is anchored at both ends.
	trimmed := strings.Trim(value, "\"'`")
	if len(trimmed) < 6 {
		return true
	}
	if placeholder.MatchString(trimmed) || reference.MatchString(trimmed) ||
		fetchedProperly.MatchString(trimmed) {
		return true
	}
	same := true
	for i := 1; i < len(trimmed); i++ {
		if trimmed[i] != trimmed[0] {
			same = false
			break
		}
	}
	return same
}

// cover takes the secrets out of a stretch of text so what is left can be
// shown. Overlaps are the normal case, not the odd one.
func cover(text string, hits []Hit) string {
	if len(hits) == 0 {
		return text
	}
	var out []byte
	at := 0
	for _, s := range hits {
		if s.From < at {
			if s.To > at {
				at = s.To
			}
			continue
		}
		if s.From > len(text) || s.To > len(text) {
			break
		}
		out = append(out, text[at:s.From]...)
		out = append(out, covered...)
		at = s.To
	}
	return string(out) + text[at:]
}

// around gives the bit of a line either side of a find, with the find taken
// out, so somebody can see what they are looking at without the report
// carrying the thing it is warning about.
//
// Bounded on both sides because a blob can be a minified bundle on one line,
// and a report is not a place to put one.
func around(body string, hit Hit) string {
	const margin = 40
	from := max(hit.From-margin, 0)
	to := min(hit.To+margin, len(body))

	// Never cross a line break: past one, the context is a different
	// statement and reads as nonsense.
	if cut := strings.LastIndexByte(body[from:hit.From], '\n'); cut >= 0 {
		from += cut + 1
	}
	if cut := strings.IndexByte(body[hit.To:to], '\n'); cut >= 0 {
		to = hit.To + cut
	}

	shown := cover(body[from:to], []Hit{{From: hit.From - from, To: hit.To - from}})
	shown = strings.TrimSpace(strings.ReplaceAll(shown, "\t", " "))
	if from > 0 {
		shown = "..." + shown
	}
	if to < len(body) {
		shown += "..."
	}
	return shown
}
