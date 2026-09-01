package main

// The pointers into the object store, and the ones people forget are
// pointers.
//
// What goes out when a repository is published is everything reachable from
// the refs that get pushed. That is branches and tags, and it is also every
// branch somebody made in 2023 and never deleted, because a branch is one
// line in a file and nobody tidies them.
//
// The rest of what is in here is the other half of the answer: the reflog,
// the stash and the config do not travel with a push, and they do travel
// with a copied folder or a zip, which is how plenty of repositories get
// handed over.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Ref struct {
	Name string
	Hash Hash
	// Kind is branch, tag, remote or something git made up.
	Kind string
}

type Repo struct {
	// Work is the working directory, and Git is the .git inside it, which
	// is not always where it looks: a worktree or a submodule leaves a
	// file that points somewhere else.
	Work string
	Git  string

	Store *Store
	Refs  []Ref
	Head  string

	// Reflog is everything the local log remembers, which includes commits
	// that no branch points at any more.
	Reflog []LogLine
	// Stash is what is sitting in the stash, which nobody remembers is
	// still there.
	Stash []Hash

	Remotes    []Remote
	Submodules []Submodule
	Ignored    []string

	Gaps []string
}

type Remote struct {
	Name string
	URL  string
}

type Submodule struct {
	Path string
	URL  string
}

type LogLine struct {
	Ref  string
	From Hash
	To   Hash
	What string
	When time.Time
	Who  string
}

func OpenRepo(where string) (*Repo, error) {
	work, git, err := findGit(where)
	if err != nil {
		return nil, err
	}
	r := &Repo{Work: work, Git: git}

	store, err := OpenStore(git)
	if err != nil {
		return nil, err
	}
	r.Store = store
	r.Gaps = append(r.Gaps, store.Gaps...)

	r.readHead()
	r.readRefs()
	r.readPackedRefs()
	r.readReflogs()
	r.readStash()
	r.readConfig()
	r.readSubmodules()
	r.readIgnored()
	return r, nil
}

func (r *Repo) Close() { r.Store.Close() }

// findGit copes with the three shapes a repository comes in: an ordinary
// one, a bare one, and the kind where .git is a file pointing elsewhere
// because it is a worktree or a submodule.
func findGit(where string) (work, git string, err error) {
	info, err := os.Stat(where)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errNotARepo
	}

	inside := filepath.Join(where, ".git")
	if at, err := os.Stat(inside); err == nil {
		if at.IsDir() {
			return where, inside, nil
		}
		body, err := os.ReadFile(inside)
		if err != nil {
			return "", "", err
		}
		if pointer, found := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:"); found {
			pointer = strings.TrimSpace(pointer)
			if !filepath.IsAbs(pointer) {
				pointer = filepath.Join(where, pointer)
			}
			return where, pointer, nil
		}
		return "", "", errNotARepo
	}

	// A bare repository is the .git directory, handed over directly.
	if _, err := os.Stat(filepath.Join(where, "HEAD")); err == nil {
		if _, err := os.Stat(filepath.Join(where, "objects")); err == nil {
			return where, where, nil
		}
	}
	return "", "", errNotARepo
}

func (r *Repo) readHead() {
	body, err := os.ReadFile(filepath.Join(r.Git, "HEAD"))
	if err != nil {
		r.Gaps = append(r.Gaps, "HEAD: "+err.Error())
		return
	}
	line := strings.TrimSpace(string(body))
	if pointer, found := strings.CutPrefix(line, "ref:"); found {
		r.Head = strings.TrimSpace(pointer)
		return
	}
	r.Head = line
}

func (r *Repo) readRefs() {
	root := filepath.Join(r.Git, "refs")
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			r.Gaps = append(r.Gaps, path+": "+err.Error())
			return nil
		}
		h, ok := hashFrom(string(body))
		if !ok {
			// A symbolic ref, which points at another ref rather than an
			// object. Following it would double count.
			return nil
		}
		rel, err := filepath.Rel(r.Git, path)
		if err != nil {
			return nil
		}
		r.Refs = append(r.Refs, Ref{Name: filepath.ToSlash(rel), Hash: h, Kind: kindOfRef(rel)})
		return nil
	})
}

// readPackedRefs reads the file git writes when it tidies the refs away,
// which is where most of the branches in an old repository actually live.
func (r *Repo) readPackedRefs() {
	file, err := os.Open(filepath.Join(r.Git, "packed-refs"))
	if err != nil {
		return
	}
	defer file.Close()

	have := map[string]bool{}
	for _, ref := range r.Refs {
		have[ref.Name] = true
	}

	lines := bufio.NewScanner(file)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		name, rest, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		h, ok := hashFrom(name)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if have[rest] {
			// A loose ref of the same name wins: it is the newer one.
			continue
		}
		r.Refs = append(r.Refs, Ref{Name: rest, Hash: h, Kind: kindOfRef(rest)})
	}
}

func kindOfRef(name string) string {
	name = filepath.ToSlash(name)
	switch {
	case strings.Contains(name, "refs/heads/"):
		return "branch"
	case strings.Contains(name, "refs/tags/"):
		return "tag"
	case strings.Contains(name, "refs/remotes/"):
		return "remote branch"
	case strings.Contains(name, "refs/stash"):
		return "stash"
	}
	return "ref"
}

// readReflogs reads the local log of where every ref has pointed.
//
// This is the part that does not travel with a push and does travel with a
// copied folder, and it is the most revealing thing in the repository: it
// names every branch that ever existed, every reset somebody did to undo
// something, and every commit that was amended away.
func (r *Repo) readReflogs() {
	root := filepath.Join(r.Git, "logs")
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		lines := bufio.NewScanner(file)
		lines.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for lines.Scan() {
			if entry, ok := readLogLine(lines.Text()); ok {
				entry.Ref = filepath.ToSlash(rel)
				r.Reflog = append(r.Reflog, entry)
			}
		}
		return nil
	})
}

// A reflog line is: <old> <new> <who> <when> <tz>\t<what>
func readLogLine(line string) (LogLine, bool) {
	before, what, found := strings.Cut(line, "\t")
	if !found {
		return LogLine{}, false
	}
	fields := strings.Fields(before)
	if len(fields) < 4 {
		return LogLine{}, false
	}
	from, okFrom := hashFrom(fields[0])
	to, okTo := hashFrom(fields[1])
	if !okFrom || !okTo {
		return LogLine{}, false
	}

	entry := LogLine{From: from, To: to, What: strings.TrimSpace(what)}
	// The middle is the name and email, and the last two are the time.
	entry.Who = strings.Join(fields[2:len(fields)-2], " ")
	entry.When = whenFrom(fields[len(fields)-2], fields[len(fields)-1])
	return entry, true
}

func (r *Repo) readStash() {
	for _, ref := range r.Refs {
		if ref.Kind == "stash" {
			r.Stash = append(r.Stash, ref.Hash)
		}
	}
	for _, entry := range r.Reflog {
		if strings.HasSuffix(entry.Ref, "stash") && !entry.To.IsZero() {
			r.Stash = append(r.Stash, entry.To)
		}
	}
}

// readConfig picks the remotes out of the config by hand, because the
// format is simple and pulling in an ini parser for four lines would be
// the only dependency in the program.
func (r *Repo) readConfig() {
	body, err := os.ReadFile(filepath.Join(r.Git, "config"))
	if err != nil {
		return
	}
	name := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			name = ""
			if inner, found := strings.CutPrefix(line, `[remote "`); found {
				name = strings.TrimSuffix(strings.TrimSuffix(inner, "]"), `"`)
			}
			continue
		}
		if name == "" {
			continue
		}
		if url, found := strings.CutPrefix(line, "url"); found {
			if value, ok := strings.CutPrefix(strings.TrimSpace(url), "="); ok {
				r.Remotes = append(r.Remotes, Remote{Name: name, URL: strings.TrimSpace(value)})
			}
		}
	}
}

func (r *Repo) readSubmodules() {
	body, err := os.ReadFile(filepath.Join(r.Work, ".gitmodules"))
	if err != nil {
		return
	}
	var current Submodule
	flush := func() {
		if current.URL != "" {
			r.Submodules = append(r.Submodules, current)
		}
		current = Submodule{}
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[submodule") {
			flush()
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "path":
			current.Path = value
		case "url":
			current.URL = value
		}
	}
	flush()
}

// readIgnored reads the ignore rules so the report can point out the file
// that is tracked in spite of them, which is how the ignore list ends up
// looking like protection it is not providing.
func (r *Repo) readIgnored() {
	body, err := os.ReadFile(filepath.Join(r.Work, ".gitignore"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(body), "\n") {
		rule := strings.TrimSpace(line)
		if rule == "" || strings.HasPrefix(rule, "#") || strings.HasPrefix(rule, "!") {
			continue
		}
		r.Ignored = append(r.Ignored, rule)
	}
}

// whenFrom turns git's seconds and offset into a time in the writer's own
// timezone, which is the point: the offset is where they were sitting.
func whenFrom(seconds, zone string) time.Time {
	n := int64(0)
	for _, c := range seconds {
		if c < '0' || c > '9' {
			return time.Time{}
		}
		n = n*10 + int64(c-'0')
	}
	at := time.Unix(n, 0).UTC()
	if len(zone) == 5 && (zone[0] == '+' || zone[0] == '-') {
		hours := int(zone[1]-'0')*10 + int(zone[2]-'0')
		minutes := int(zone[3]-'0')*10 + int(zone[4]-'0')
		offset := hours*3600 + minutes*60
		if zone[0] == '-' {
			offset = -offset
		}
		return at.In(time.FixedZone(zone, offset))
	}
	return at
}
