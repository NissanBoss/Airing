package main

// Turning the history into a report.
//
// Everything is sorted into two piles and they are never mixed, because
// mixing them is what makes a tool like this either frightening or useless.
// One pile goes out the moment somebody presses the button. The other only
// goes if they hand over the folder, which is a different decision on a
// different day, and telling somebody their reflog is about to be published
// when it is not would be a lie that costs the rest of the report its
// credibility.

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

var errNotARepo = errors.New("there is no git repository here")

type Airing struct {
	Repo    *Repo
	History *History

	// Found is every credential in a blob a push would send.
	Found []Find
	// Kept is the same for the objects nothing points at.
	Kept []Find

	// Hosts is every internal looking hostname found in a file, and the
	// files it was found in.
	Hosts map[string]map[string]bool

	KeyBlobs map[Hash]bool
	Gaps     []string
}

// Find is one credential in one blob, kept as it will be printed rather
// than as it was read. The covering happens the moment the blob comes out
// of the store, so there is no later point where the original could be
// printed by mistake.
type Find struct {
	Blob  Hash
	Path  string
	Rule  int
	Shown string
	// StillThere says the file is in the finished repository as well as in
	// the history, which is the difference between a mistake somebody can
	// see and one nobody has looked at since.
	StillThere bool
	// When is the commit that first carried it.
	When Hash
}

// biggestBlob is as much of one object as this will read looking for a
// secret. A history holds every version of everything, so a repository with
// a vendored dependency tree has a great many megabytes in it and none of
// the interesting ones are large.
const biggestBlob = 4 << 20

func inspect(r *Repo) *Airing {
	a := &Airing{
		Repo:     r,
		KeyBlobs: map[Hash]bool{},
		Hosts:    map[string]map[string]bool{},
		Gaps:     r.Gaps,
	}
	a.History = Walk(r)
	a.Gaps = append(a.Gaps, a.History.Gaps...)

	// Sorted so that two runs over the same repository come out the same
	// way, which map iteration would not give.
	blobs := make([]*Placed, 0, len(a.History.Blobs))
	for _, placed := range a.History.Blobs {
		blobs = append(blobs, placed)
	}
	sort.Slice(blobs, func(i, j int) bool {
		return blobs[i].Hash.String() < blobs[j].Hash.String()
	})

	for _, placed := range blobs {
		where := somePath(placed)
		if isTemplate(where) {
			continue
		}
		for _, f := range a.lookIn(placed.Hash, where) {
			f.StillThere = placed.InHead
			f.When = placed.First
			a.Found = append(a.Found, f)
		}
	}

	for _, at := range a.History.Loose {
		o, err := r.Store.Read(at)
		if err != nil || o.Kind != Blob {
			continue
		}
		for _, f := range a.lookIn(at, "") {
			a.Kept = append(a.Kept, f)
		}
	}
	return a
}

func (a *Airing) lookIn(blob Hash, where string) []Find {
	o, err := a.Repo.Store.Read(blob)
	if err != nil || o.Kind != Blob || len(o.Body) == 0 || len(o.Body) > biggestBlob {
		return nil
	}
	if !readable(o.Body) {
		return nil
	}
	body := string(o.Body)
	hits := detect(body)

	// The hostnames come out of the covered text rather than the original,
	// so a password sitting on the same line as one cannot travel back out
	// through this route. It costs one more pass over the blob and it means
	// there is no code path where the raw body reaches a report.
	if where != "" {
		a.noteHosts(cover(body, hits), where)
	}

	var out []Find
	for _, hit := range hits {
		if detectors[hit.Rule].Name == "a private key" {
			a.KeyBlobs[blob] = true
		}
		out = append(out, Find{
			Blob: blob, Path: where, Rule: hit.Rule, Shown: around(body, hit),
		})
	}
	return out
}

func (a *Airing) noteHosts(text, where string) {
	for _, m := range insideFile.FindAllStringSubmatch(text, -1) {
		host := strings.ToLower(m[1])
		if a.Hosts[host] == nil {
			a.Hosts[host] = map[string]bool{}
		}
		a.Hosts[host][where] = true
	}
}

// readable keeps the rules off compiled things, where a long run of bytes
// matches everything eventually.
func readable(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	printable := 0
	for _, b := range head {
		if b == 0 {
			return false
		}
		if b >= 0x20 && b < 0x7f || b == '\n' || b == '\r' || b == '\t' {
			printable++
		}
	}
	return printable*10 >= len(head)*9
}

func somePath(p *Placed) string {
	var all []string
	for where := range p.Paths {
		all = append(all, where)
	}
	sort.Strings(all)
	if len(all) == 0 {
		return ""
	}
	return all[0]
}

func (a *Airing) writeUp(reveal bool) *Report {
	r := &Report{}
	for _, g := range a.Gaps {
		r.gap(g)
	}

	a.secretsGoing(r, reveal)
	a.filesGone(r)
	a.namedFiles(r)
	a.heavy(r)
	a.submodules(r, reveal)

	a.handedOver(r, reveal)

	a.whoWroteIt(r, reveal)
	a.namesAndMessages(r, reveal)
	a.insideNames(r, reveal)
	a.ignoredAnyway(r)
	a.inventory(r)

	if r.worst() < Public {
		r.add(Finding{
			Severity: Note,
			Title:    "Nothing turned up, which is not the same as nothing being there",
			Advice: "This reads the text blobs under four megabytes, with rules " +
				"that each name what they find, and it leaves the test fixtures " +
				"and example files alone. A key that looks like nothing in " +
				"particular, in a compiled artefact, or in a submodule this " +
				"cannot see, is somewhere it never looked.",
		})
	}
	return r
}

// secretsGoing is the first pile: what the push sends.
func (a *Airing) secretsGoing(r *Report, reveal bool) {
	byPath := map[string][]Find{}
	var order []string
	for _, f := range a.Found {
		if detectors[f.Rule].Sort == Account {
			continue
		}
		where := f.Path
		if where == "" {
			where = "a blob with no path in the history"
		}
		if _, seen := byPath[where]; !seen {
			order = append(order, where)
		}
		byPath[where] = append(byPath[where], f)
	}
	sort.Strings(order)

	var tests group
	inTests := 0
	for _, where := range order {
		found := byPath[where]
		if !looksLikeATest(where) {
			continue
		}
		inTests += len(found)
		tests.add(where, plural(len(found), "secret", "secrets"))
	}
	if tests.count > 0 {
		r.add(Finding{
			Severity: Public,
			Title:    plural(inTests, "secret is", "secrets are") + " in files that look like tests",
			Lines:    atMost(tests.lines, 16),
			Advice: "Together rather than one finding each, because most fixtures " +
				"are made up and calling every one of them a credential to rotate " +
				"is how a report gets skipped. The ones that are not made up are " +
				"the worst kind there is: a test that talks to something real " +
				"needs a credential that works, and nobody opens a test file " +
				"before publishing.",
		})
	}

	for _, where := range order {
		if looksLikeATest(where) {
			continue
		}
		found := byPath[where]
		var rows group
		gone := false
		for _, f := range found {
			state := "still in the repository"
			if !f.StillThere {
				state = "taken out later, and still on the branch"
				gone = true
			}
			rows.add(detectors[f.Rule].Name, state)
			if reveal {
				rows.lines = append(rows.lines, "  "+f.Shown)
				if info, ok := a.History.Commits[f.When]; ok {
					rows.lines = append(rows.lines,
						"  put there by "+show(info.Author.Email, reveal)+
							" in "+f.When.Short())
				}
			}
		}

		advice := "Rotate it. Taking it out of the tree does not take it off " +
			"the branch, and rewriting the history does not help anybody who " +
			"cloned before you did."
		if gone {
			advice = "This is the one people get wrong. The file is not in the " +
				"repository any more and the commit that added it still is, so " +
				"it goes out with the push and one command brings it back. " +
				"Rotate the credential first and worry about the history after."
		}
		if !reveal {
			advice += " Run again with --reveal to see the lines and who put " +
				"them there, with the secrets themselves still covered over."
		}

		r.add(Finding{
			Severity: Public,
			Title:    plural(rows.count, "secret is", "secrets are") + " in " + where,
			Lines:    atMost(rows.lines, 20),
			Advice:   advice,
		})
	}
}

// filesGone is the finding this tool exists for.
func (a *Airing) filesGone(r *Report) {
	gone := a.History.Gone()
	if len(gone) == 0 {
		return
	}
	sort.Slice(gone, func(i, j int) bool {
		return somePath(gone[i]) < somePath(gone[j])
	})

	var worrying group
	ordinary := 0
	for _, placed := range gone {
		where := somePath(placed)
		kit, is := carries(where)
		if !is || isTemplate(where) {
			ordinary++
			continue
		}
		if kit.NeedsKey && !a.KeyBlobs[placed.Hash] {
			ordinary++
			continue
		}
		when := ""
		if info, found := a.History.Commits[placed.First]; found && !info.Author.When.IsZero() {
			when = ", added " + info.Author.When.Format("2 January 2006")
		}
		worrying.add(where, kit.What+when)
		worrying.lines = append(worrying.lines, folded("  ", kit.Why)...)
	}

	if worrying.count > 0 {
		r.add(Finding{
			Severity: Public,
			Title: plural(worrying.count, "file was taken out and goes out anyway",
				"files were taken out and go out anyway"),
			Lines: atMost(worrying.lines, 24),
			Advice: "None of these is in the repository now and all of them are " +
				"on the branch. Anybody who clones it can walk back to the " +
				"commit that had them, and the tools that do that automatically " +
				"are older than most of the repositories they are pointed at.",
		})
	}
	if ordinary > 0 {
		r.add(Finding{
			Severity: Note,
			Title: fmt.Sprintf("%s in the history and not in the repository",
				plural(ordinary, "other file is", "other files are")),
			Advice: "Ordinary churn: renames, refactors, things that were tried " +
				"and dropped. They are counted rather than listed because the " +
				"count is what says how much of the history is not the tree.",
		})
	}
}

// namedFiles is what is in the repository now that should not be published.
func (a *Airing) namedFiles(r *Report) {
	var grave, untidy group
	seen := map[string]bool{}

	paths := make([]string, 0, len(a.History.HeadPaths))
	for where := range a.History.HeadPaths {
		paths = append(paths, where)
	}
	sort.Strings(paths)

	for _, where := range paths {
		if isTemplate(where) || seen[where] {
			continue
		}
		kit, is := carries(where)
		if !is {
			continue
		}
		if kit.NeedsKey && !a.KeyBlobs[a.History.HeadPaths[where]] {
			continue
		}
		seen[where] = true
		row := &untidy
		if kit.Grave {
			row = &grave
		}
		row.add(where, kit.What)
		row.lines = append(row.lines, folded("  ", kit.Why)...)
	}

	if grave.count > 0 {
		r.add(Finding{
			Severity: Public,
			Title:    plural(grave.count, "file is", "files are") + " here that must not be published",
			Lines:    atMost(grave.lines, 24),
			Advice: "These are in the repository as it stands, so they need no " +
				"digging at all: anybody who clones it has them.",
		})
	}
	if untidy.count > 0 {
		r.add(Finding{
			Severity: Handed,
			Title:    plural(untidy.count, "file came along", "files came along") + " that nobody meant to publish",
			Lines:    atMost(untidy.lines, 24),
			Advice: "None of these is a credential. They are weight, and they " +
				"describe the project and the machine to anybody who looks.",
		})
	}
}

// heavy is the practical half: a public repository is a repository people
// clone, and the history is what they have to download.
func (a *Airing) heavy(r *Report) {
	type big struct {
		where string
		size  int
		head  bool
	}
	var found []big
	total := 0
	for _, placed := range a.History.Blobs {
		total += placed.Size
		if placed.Size >= 5<<20 {
			found = append(found, big{somePath(placed), placed.Size, placed.InHead})
		}
	}
	if len(found) == 0 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].size > found[j].size })

	var rows group
	for _, b := range found[:min(len(found), 10)] {
		state := size(b.size)
		if !b.head {
			state += ", and not in the repository any more"
		}
		rows.add(b.where, state)
	}
	r.add(Finding{
		Severity: Note,
		Title: fmt.Sprintf("%s over five megabytes, out of %s of history",
			plural(len(found), "blob is", "blobs are"), size(total)),
		Lines: atMost(rows.lines, 12),
		Advice: "Every clone downloads all of it, including the versions that " +
			"were deleted. A big file that is no longer in the tree is the " +
			"worst of both: nobody gets any use out of it and everybody pays " +
			"for it.",
	})
}

func (a *Airing) submodules(r *Report, reveal bool) {
	var rows group
	for _, s := range a.Repo.Submodules {
		if !privateLooking(s.URL) {
			continue
		}
		rows.add(s.Path, show(s.URL, reveal))
	}
	if rows.count == 0 {
		return
	}
	r.add(Finding{
		Severity: Public,
		Title:    plural(rows.count, "submodule points", "submodules point") + " somewhere not public",
		Lines:    atMost(rows.lines, 8),
		Advice: "The submodule itself stays private and its address does not. " +
			"A public repository naming a private one tells everybody that " +
			"repository exists, what it is called and who hosts it, which is " +
			"three things they did not have before.",
	})
}

// handedOver is the second pile, and it is a different sentence.
func (a *Airing) handedOver(r *Report, reveal bool) {
	loose := len(a.History.Loose)
	if loose > 0 {
		var rows group
		rows.add(plural(loose, "object", "objects"), "reachable from nothing")
		if len(a.Kept) > 0 {
			rows.add(plural(len(a.Kept), "of them holds a secret", "of them hold a secret"),
				"and a push would not send any of it")
		}
		r.add(Finding{
			Severity: Handed,
			Title: plural(loose, "object is in the store and on no branch",
				"objects are in the store and on no branch"),
			Lines: atMost(rows.lines, 8),
			Advice: "Amended commits, deleted branches, dropped stashes. A push " +
				"does not send these, so publishing the repository does not " +
				"publish them, and copying the folder or handing over a zip " +
				"does. That is a different decision and worth knowing it is a " +
				"different one.",
		})
	}

	if len(a.Repo.Reflog) > 0 {
		branches := map[string]bool{}
		resets := 0
		for _, entry := range a.Repo.Reflog {
			branches[entry.Ref] = true
			if strings.HasPrefix(entry.What, "reset:") || strings.Contains(entry.What, "amend") {
				resets++
			}
		}
		var rows group
		rows.add(plural(len(a.Repo.Reflog), "line", "lines"), "in the local log")
		rows.add(plural(len(branches), "ref", "refs"), "it remembers pointing somewhere")
		if resets > 0 {
			rows.add(plural(resets, "reset or amend", "resets and amends"),
				"where something was undone")
		}
		r.add(Finding{
			Severity: Handed,
			Title:    "the reflog remembers what the branches used to be",
			Lines:    rows.lines,
			Advice: "Every branch that ever existed by name, every reset that " +
				"undid something, and when. It does not travel with a push and " +
				"it does travel with the folder, and it is the most revealing " +
				"file in the repository for its size.",
		})
	}

	if len(a.Repo.Stash) > 0 {
		r.add(Finding{
			Severity: Handed,
			Title:    plural(len(a.Repo.Stash), "thing is", "things are") + " in the stash",
			Advice: "Work somebody put aside and did not come back to. It is on " +
				"no branch, so it does not get pushed, and stashes are where " +
				"the half finished experiment with the real credentials in it " +
				"tends to end up.",
		})
	}

	var creds group
	for _, remote := range a.Repo.Remotes {
		if hits := detect(remote.URL); len(hits) > 0 {
			creds.add(remote.Name, "the URL has a credential in it")
			continue
		}
		if privateLooking(remote.URL) {
			creds.add(remote.Name, show(remote.URL, reveal))
		}
	}
	if creds.count > 0 {
		r.add(Finding{
			Severity: Handed,
			Title:    plural(creds.count, "remote is", "remotes are") + " worth a look before this leaves your machine",
			Lines:    atMost(creds.lines, 8),
			Advice: "The config does not get pushed. It does get copied, and a " +
				"remote URL with a token in it is a working credential written " +
				"in a file most people have never opened.",
		})
	}
}

// whoWroteIt is what the history says about the people in it.
func (a *Airing) whoWroteIt(r *Report, reveal bool) {
	type who struct {
		emails map[string]bool
		first  time.Time
		last   time.Time
		count  int
	}
	people := map[string]*who{}
	domains := map[string]int{}
	var hours [24]int
	timed := 0

	for _, at := range a.History.Order {
		info := a.History.Commits[at]
		name := info.Author.Name
		if name == "" {
			name = info.Author.Email
		}
		p, found := people[name]
		if !found {
			p = &who{emails: map[string]bool{}}
			people[name] = p
		}
		p.count++
		if info.Author.Email != "" {
			p.emails[info.Author.Email] = true
			if _, domain, ok := strings.Cut(info.Author.Email, "@"); ok {
				domains[strings.ToLower(domain)]++
			}
		}
		if !info.Author.When.IsZero() {
			hours[info.Author.When.Hour()]++
			timed++
			if p.first.IsZero() || info.Author.When.Before(p.first) {
				p.first = info.Author.When
			}
			if info.Author.When.After(p.last) {
				p.last = info.Author.When
			}
		}
	}
	if len(people) == 0 {
		return
	}

	names := make([]string, 0, len(people))
	for name := range people {
		names = append(names, name)
	}
	sort.Strings(names)

	var rows group
	for _, name := range names {
		p := people[name]
		what := plural(p.count, "commit", "commits")
		if len(p.emails) > 1 {
			what += fmt.Sprintf(", under %d addresses", len(p.emails))
		}
		if !p.first.IsZero() {
			what += ", " + p.first.Format("Jan 2006") + " to " + p.last.Format("Jan 2006")
		}
		rows.add(show(name, reveal), what)
	}

	// The domains go in under the people rather than only in the count,
	// because the address is the part that ties the repository to an
	// employer and the name is the part somebody already knew.
	names = names[:0]
	for domain := range domains {
		names = append(names, domain)
	}
	sort.Strings(names)
	if len(names) > 0 {
		rows.lines = append(rows.lines, "")
		for _, domain := range names {
			rows.lines = append(rows.lines,
				columns(show(domain, reveal), plural(domains[domain], "commit", "commits"))...)
		}
	}

	advice := "Every one of these goes out with the history and none of it can " +
		"be taken back afterwards. Somebody who used a work address on two " +
		"commits in 2023 has tied this repository to that employer for good."
	if from, to, ok := awake(hours, timed); ok {
		advice = fmt.Sprintf("The timestamps put the work between %02d:00 and "+
			"%02d:59, which gives away the timezone whatever anybody's profile "+
			"says. %s", from, to, advice)
	}

	r.add(Finding{
		Severity: Tells,
		Title: fmt.Sprintf("%s and %s in the history",
			plural(len(people), "person", "people"),
			plural(len(domains), "email domain", "email domains")),
		Lines:  atMost(rows.lines, 16),
		Advice: advice,
	})
}

// awake finds the stretch of the day the commits cluster into, wrapping
// round midnight, because somebody who works evenings has their quiet hours
// in the middle of the list and reading it flat says midnight to eleven.
func awake(hours [24]int, total int) (from, to int, ok bool) {
	if total < 20 {
		return 0, 0, false
	}
	floor := total / 40
	busy := map[int]bool{}
	count := 0
	for hour, n := range hours {
		if n > floor {
			busy[hour] = true
			count++
		}
	}
	if count == 0 || count == 24 {
		return 0, 0, false
	}
	best, run := -1, 0
	for start := range 24 {
		if busy[(start+23)%24] {
			continue
		}
		n := 0
		for n < 24 && busy[(start+n)%24] {
			n++
		}
		if n > run {
			best, run = start, n
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return best, (best + run - 1) % 24, true
}

// namesAndMessages is the part people never think of, because a branch name
// is not a file and a commit message is not code.
func (a *Airing) namesAndMessages(r *Report, reveal bool) {
	var rows group
	seen := map[string]bool{}

	add := func(what, where string) {
		if what == "" || seen[what] {
			return
		}
		if ticket.MatchString(what) && !isTicket(what) {
			return
		}
		seen[what] = true
		rows.add(show(what, reveal), where)
	}

	for _, ref := range a.Repo.Refs {
		name := path.Base(ref.Name)
		for _, m := range internalWord.FindAllString(ref.Name, -1) {
			add(m, "in the name of a "+ref.Kind)
		}
		_ = name
	}
	for _, at := range a.History.Order {
		message := a.History.Commits[at].Message
		for _, m := range insideHost.FindAllString(message, -1) {
			add(m, "in a commit message")
		}
		for _, m := range ticket.FindAllString(message, -1) {
			add(m, "in a commit message")
		}
	}
	if rows.count == 0 {
		return
	}
	r.add(Finding{
		Severity: Tells,
		Title: plural(rows.count, "internal reference is", "internal references are") +
			" in the names and the messages",
		Lines: atMost(rows.lines, 16),
		Advice: "Branch names and commit messages go out with everything else " +
			"and nobody reads them back before publishing. A ticket number is " +
			"a tracker somebody can go and look for, and an internal hostname " +
			"is a description of the network on the other side.",
	})
}

// insideNames is the hostnames written into the files themselves, which is
// the half of this that survives whatever pass somebody makes over the
// branch names and the commit messages before publishing.
func (a *Airing) insideNames(r *Report, reveal bool) {
	if len(a.Hosts) == 0 {
		return
	}
	hosts := make([]string, 0, len(a.Hosts))
	for host := range a.Hosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	var rows group
	for _, host := range hosts {
		where := make([]string, 0, len(a.Hosts[host]))
		for path := range a.Hosts[host] {
			where = append(where, path)
		}
		sort.Strings(where)
		what := "in " + where[0]
		if len(where) > 1 {
			what += fmt.Sprintf(" and %s", plural(len(where)-1, "other file", "other files"))
		}
		rows.add(show(host, reveal), what)
	}

	r.add(Finding{
		Severity: Tells,
		Title: plural(rows.count, "internal hostname is", "internal hostnames are") +
			" written into the files",
		Lines: atMost(rows.lines, 16),
		Advice: "A name nothing outside can resolve is a description of the " +
			"network on the other side: what the machines are called, how they " +
			"are grouped, and which of them this code talks to. It is in the " +
			"history as well as in the tree, so editing the file now leaves it " +
			"exactly where it was.",
	})
}

func (a *Airing) ignoredAnyway(r *Report) {
	if len(a.Repo.Ignored) == 0 {
		return
	}
	var rows group
	paths := make([]string, 0, len(a.History.HeadPaths))
	for where := range a.History.HeadPaths {
		paths = append(paths, where)
	}
	sort.Strings(paths)

	for _, where := range paths {
		for _, rule := range a.Repo.Ignored {
			if matchesIgnore(rule, where) {
				rows.add(where, "matches "+rule)
				break
			}
		}
	}
	if rows.count == 0 {
		return
	}
	r.add(Finding{
		Severity: Note,
		Title: plural(rows.count, "tracked file matches", "tracked files match") +
			" a rule in the gitignore",
		Lines: atMost(rows.lines, 12),
		Advice: "The ignore list only stops a file being added. Once something " +
			"is tracked, git carries on tracking it and the rule does nothing, " +
			"so the list reads like protection it is not providing.",
	})
}

func (a *Airing) inventory(r *Report) {
	branches, tags := 0, 0
	for _, ref := range a.Repo.Refs {
		switch ref.Kind {
		case "branch":
			branches++
		case "tag":
			tags++
		}
	}
	lines := columns("commits", plural(len(a.History.Commits), "commit", "commits"))
	lines = append(lines, columns("refs", fmt.Sprintf("%s and %s",
		plural(branches, "branch", "branches"), plural(tags, "tag", "tags")))...)
	lines = append(lines, columns("objects", fmt.Sprintf("%s a push would send, %s it would not",
		plural(len(a.History.Reachable), "object", "objects"),
		plural(len(a.History.Loose), "object", "objects")))...)
	lines = append(lines, columns("files now", plural(len(a.History.HeadPaths), "file", "files"))...)
	lines = append(lines, columns("blobs ever", plural(len(a.History.Blobs), "blob", "blobs"))...)

	r.add(Finding{Severity: Note, Title: "what is in here", Lines: lines})
}

func size(bytes int) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(bytes)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", bytes)
}

func (a *Airing) summary() string {
	parts := []string{plural(len(a.History.Commits), "commit", "commits")}
	parts = append(parts, plural(len(a.History.Blobs), "blob", "blobs"))
	going := 0
	for _, f := range a.Found {
		if detectors[f.Rule].Sort != Account {
			going++
		}
	}
	if going > 0 {
		parts = append(parts, plural(going, "secret", "secrets"))
	}
	return strings.Join(parts, ", ")
}

func (a *Airing) leaking() bool {
	for _, f := range a.Found {
		if detectors[f.Rule].Sort != Account {
			return true
		}
	}
	return false
}
