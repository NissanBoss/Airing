package main

// Walking the history, and working out what is reachable from where.
//
// The whole report turns on one distinction that almost nobody draws, so it
// is worth being exact about it here.
//
// A push sends everything reachable from the refs being pushed. That is
// every commit on every branch and tag, every tree those commits point at,
// and every blob in those trees. A file deleted in 2023 is still reachable
// through the commit that had it, so it goes. That is the half people get
// wrong.
//
// A push does not send anything unreachable: the blob from an amended
// commit, the branch somebody deleted, the dropped stash. Those sit in the
// local object store until git gets around to collecting them, and they go
// nowhere, unless somebody hands over the folder rather than pushing, which
// happens more than it should.
//
// So everything here is sorted into those two piles and the report keeps
// them apart.

import (
	"bytes"
	"strings"
	"time"
)

type Person struct {
	Name  string
	Email string
	When  time.Time
}

type CommitInfo struct {
	Hash      Hash
	Tree      Hash
	Parents   []Hash
	Author    Person
	Committer Person
	Message   string
	Signed    bool
}

type TreeEntry struct {
	Mode string
	Name string
	Hash Hash
}

// Placed is one blob and every path it has ever been at, with the commits
// that put it there.
type Placed struct {
	Hash  Hash
	Paths map[string]bool
	// First and Last are the commits that introduced and last carried it.
	First Hash
	Size  int
	// InHead is whether the finished repository still has it.
	InHead bool
}

type History struct {
	Repo *Repo

	Commits map[Hash]CommitInfo
	// Order is the commits in the order they were walked, so that reports
	// come out the same way twice.
	Order []Hash

	// Reachable is every object a push would send.
	Reachable map[Hash]bool
	// Blobs is every blob a push would send, with where it has lived.
	Blobs map[Hash]*Placed
	// HeadPaths is what the finished repository actually contains.
	HeadPaths map[string]Hash

	// Loose is what is in the object store and reachable from nothing: the
	// amended commits, the deleted branches, the dropped stashes.
	Loose []Hash

	Gaps []string
}

// deepestTree stops a tree that points at itself, which a hand made object
// can do and a real one cannot.
const deepestTree = 64

func Walk(r *Repo) *History {
	h := &History{
		Repo:      r,
		Commits:   map[Hash]CommitInfo{},
		Reachable: map[Hash]bool{},
		Blobs:     map[Hash]*Placed{},
		HeadPaths: map[string]Hash{},
	}

	for _, ref := range r.Refs {
		// The stash is a ref and a push does not send it, so what is in it
		// belongs in the other pile with everything else that only travels
		// with the folder. Walking it here would have the report saying
		// the half finished experiment with the real credentials in it was
		// about to be published, and that is the one thing a report like
		// this cannot afford to get wrong. Its objects are still read:
		// they come out of the store as reachable from nothing, which is
		// exactly what they are.
		if ref.Kind == "stash" {
			continue
		}
		h.fromCommit(ref.Hash)
	}

	h.readHeadTree()
	h.findLoose()
	return h
}

// fromCommit walks a commit and everything under it, stopping wherever it
// has been before. Iterative rather than recursive: a repository with fifty
// thousand commits would put fifty thousand frames on the stack.
func (h *History) fromCommit(start Hash) {
	queue := []Hash{start}
	for len(queue) > 0 {
		at := queue[len(queue)-1]
		queue = queue[:len(queue)-1]

		if h.Reachable[at] {
			continue
		}
		o, err := h.Repo.Store.Read(at)
		if err != nil {
			continue
		}
		h.Reachable[at] = true

		switch o.Kind {
		case Tag:
			if target, ok := taggedBy(o.Body); ok {
				queue = append(queue, target)
			}
		case Commit:
			info, ok := readCommit(at, o.Body)
			if !ok {
				h.Gaps = append(h.Gaps, at.Short()+": the commit will not parse")
				continue
			}
			h.Commits[at] = info
			h.Order = append(h.Order, at)
			queue = append(queue, info.Parents...)
			h.fromTree(info.Tree, "", at, 0)
		case Blob:
			// A ref pointing straight at a blob is unusual and legal.
			h.note(at, "", start, len(o.Body))
		}
	}
}

func (h *History) fromTree(root Hash, prefix string, commit Hash, depth int) {
	if depth > deepestTree || h.Reachable[root] {
		return
	}
	o, err := h.Repo.Store.Read(root)
	if err != nil || o.Kind != Tree {
		return
	}
	h.Reachable[root] = true

	for _, e := range readTree(o.Body) {
		path := e.Name
		if prefix != "" {
			path = prefix + "/" + e.Name
		}
		switch {
		case e.Mode == "160000":
			// A submodule: a commit hash recorded in the tree, and the
			// object itself lives in another repository.
			continue
		case isTree(e.Mode):
			h.fromTree(e.Hash, path, commit, depth+1)
		default:
			h.Reachable[e.Hash] = true
			size := 0
			if o, err := h.Repo.Store.Read(e.Hash); err == nil {
				size = len(o.Body)
			}
			h.note(e.Hash, path, commit, size)
		}
	}
}

func (h *History) note(blob Hash, path string, commit Hash, size int) {
	placed, found := h.Blobs[blob]
	if !found {
		placed = &Placed{Hash: blob, Paths: map[string]bool{}, First: commit, Size: size}
		h.Blobs[blob] = placed
	}
	if path != "" {
		placed.Paths[path] = true
	}
}

// readHeadTree records what the finished repository looks like, so that
// everything else in the history can be told apart from it.
func (h *History) readHeadTree() {
	head := h.Repo.Head
	var at Hash
	if direct, ok := hashFrom(head); ok {
		at = direct
	} else {
		for _, ref := range h.Repo.Refs {
			if ref.Name == head {
				at = ref.Hash
			}
		}
	}
	if at.IsZero() {
		return
	}
	info, found := h.Commits[at]
	if !found {
		return
	}
	h.headTree(info.Tree, "", 0)

	for path, blob := range h.HeadPaths {
		if placed, found := h.Blobs[blob]; found {
			placed.InHead = true
			placed.Paths[path] = true
		}
	}
}

func (h *History) headTree(root Hash, prefix string, depth int) {
	if depth > deepestTree {
		return
	}
	o, err := h.Repo.Store.Read(root)
	if err != nil || o.Kind != Tree {
		return
	}
	for _, e := range readTree(o.Body) {
		path := e.Name
		if prefix != "" {
			path = prefix + "/" + e.Name
		}
		if isTree(e.Mode) {
			h.headTree(e.Hash, path, depth+1)
			continue
		}
		if e.Mode == "160000" {
			continue
		}
		h.HeadPaths[path] = e.Hash
	}
}

// findLoose is everything in the store that nothing points at.
func (h *History) findLoose() {
	for _, at := range h.Repo.Store.Everything() {
		if !h.Reachable[at] {
			h.Loose = append(h.Loose, at)
		}
	}
}

// Gone lists the blobs that are in the history and not in the finished
// repository: the file somebody committed and deleted, which goes out with
// the push exactly the same.
func (h *History) Gone() []*Placed {
	var out []*Placed
	for _, placed := range h.Blobs {
		if !placed.InHead && len(placed.Paths) > 0 {
			out = append(out, placed)
		}
	}
	return out
}

// readCommit picks a commit apart. The header is one field per line until a
// blank one, and everything after that is the message.
func readCommit(at Hash, body []byte) (CommitInfo, bool) {
	info := CommitInfo{Hash: at}
	head, message, found := bytes.Cut(body, []byte("\n\n"))
	if !found {
		head = body
	}
	info.Message = strings.TrimSpace(string(message))

	for _, line := range strings.Split(string(head), "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch key {
		case "tree":
			if h, ok := hashFrom(value); ok {
				info.Tree = h
			}
		case "parent":
			if h, ok := hashFrom(value); ok {
				info.Parents = append(info.Parents, h)
			}
		case "author":
			info.Author = readPerson(value)
		case "committer":
			info.Committer = readPerson(value)
		case "gpgsig":
			info.Signed = true
		}
	}
	return info, !info.Tree.IsZero()
}

// readPerson reads "Name <email> 1725000000 +0200".
func readPerson(line string) Person {
	var p Person
	open := strings.LastIndex(line, "<")
	close := strings.LastIndex(line, ">")
	if open >= 0 && close > open {
		p.Name = strings.TrimSpace(line[:open])
		p.Email = strings.TrimSpace(line[open+1 : close])
		rest := strings.Fields(line[close+1:])
		if len(rest) >= 2 {
			p.When = whenFrom(rest[0], rest[1])
		}
		return p
	}
	p.Name = strings.TrimSpace(line)
	return p
}

// readTree walks the entries of a tree, which are "<mode> <name>\0<20 raw
// bytes>" one after another with nothing between them.
func readTree(body []byte) []TreeEntry {
	var out []TreeEntry
	at := 0
	for at < len(body) {
		space := bytes.IndexByte(body[at:], ' ')
		if space < 0 {
			break
		}
		mode := string(body[at : at+space])
		at += space + 1

		end := bytes.IndexByte(body[at:], 0)
		if end < 0 || at+end+1+20 > len(body) {
			break
		}
		name := string(body[at : at+end])
		at += end + 1

		var h Hash
		copy(h[:], body[at:at+20])
		at += 20

		out = append(out, TreeEntry{Mode: mode, Name: name, Hash: h})
	}
	return out
}

// taggedBy finds what an annotated tag points at.
func taggedBy(body []byte) (Hash, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		if value, found := strings.CutPrefix(line, "object "); found {
			return hashFrom(value)
		}
		if line == "" {
			break
		}
	}
	return Hash{}, false
}

// isTree says whether a tree entry is a directory.
//
// The mode is written as octal with no padding, so a directory is "40000"
// and not "040000". Checking for a leading zero instead meant no
// subdirectory was ever walked, and a fifth of a real repository came out
// as unreachable because everything below the top level had been missed.
// A clone straight off a server has nothing unreachable in it, which is
// what made the number obviously wrong.
func isTree(mode string) bool {
	return mode == "40000" || mode == "040000"
}
