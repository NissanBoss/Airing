package main

// Building repositories by hand, so the tests do not need git installed.
//
// A loose object is the type, a space, the length, a zero byte and the
// body, deflated, filed under the first two characters of its own sha1.
// That is the whole format, and writing it here rather than shelling out
// to git means the tests run the same on a machine that has never had git
// on it, which is the machine the CI starts from.

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type builder struct {
	t    *testing.T
	work string
	git  string
}

func newRepo(t *testing.T) *builder {
	t.Helper()
	work := t.TempDir()
	git := filepath.Join(work, ".git")
	for _, dir := range []string{"objects", "refs/heads", "refs/tags", "logs/refs/heads"} {
		if err := os.MkdirAll(filepath.Join(git, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := &builder{t: t, work: work, git: git}
	b.write("HEAD", "ref: refs/heads/main\n")
	return b
}

func (b *builder) write(name, body string) {
	b.t.Helper()
	path := filepath.Join(b.git, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		b.t.Fatal(err)
	}
}

// store writes one object and gives back its name, which is the hash of
// the header and the body together and not of the body alone.
func (b *builder) store(kind Kind, body []byte) Hash {
	b.t.Helper()
	header := fmt.Sprintf("%s %d\x00", kind, len(body))
	whole := append([]byte(header), body...)
	h := Hash(sha1.Sum(whole))

	name := h.String()
	dir := filepath.Join(b.git, "objects", name[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.t.Fatal(err)
	}
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	w.Write(whole)
	w.Close()
	if err := os.WriteFile(filepath.Join(dir, name[2:]), packed.Bytes(), 0o644); err != nil {
		b.t.Fatal(err)
	}
	return h
}

func (b *builder) blob(body string) Hash { return b.store(Blob, []byte(body)) }

// tree takes paths with slashes in them and builds the subtrees, because
// writing them out by hand in every test is where the mistakes go.
func (b *builder) tree(files map[string]Hash) Hash {
	b.t.Helper()
	here := map[string]Hash{}
	under := map[string]map[string]Hash{}

	for path, h := range files {
		dir, rest, deeper := strings.Cut(path, "/")
		if !deeper {
			here[path] = h
			continue
		}
		if under[dir] == nil {
			under[dir] = map[string]Hash{}
		}
		under[dir][rest] = h
	}

	type entry struct {
		mode string
		name string
		hash Hash
	}
	var all []entry
	for name, h := range here {
		all = append(all, entry{"100644", name, h})
	}
	for name, files := range under {
		all = append(all, entry{"40000", name, b.tree(files)})
	}
	// Git sorts the entries and so must this: a tree with them in another
	// order hashes to something else and is a different object.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && sortsBefore(all[j], all[j-1]); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}

	var body bytes.Buffer
	for _, e := range all {
		fmt.Fprintf(&body, "%s %s\x00", e.mode, e.name)
		body.Write(e.hash[:])
	}
	return b.store(Tree, body.Bytes())
}

func sortsBefore(a, bb struct {
	mode string
	name string
	hash Hash
}) bool {
	left, right := a.name, bb.name
	if a.mode == "40000" {
		left += "/"
	}
	if bb.mode == "40000" {
		right += "/"
	}
	return left < right
}

type commitOpts struct {
	tree    Hash
	parents []Hash
	author  string
	when    string
	message string
}

func (b *builder) commit(o commitOpts) Hash {
	b.t.Helper()
	if o.author == "" {
		o.author = "Ana Ruiz <ana@acme.corp>"
	}
	if o.when == "" {
		o.when = "1725190000 +0200"
	}
	var body bytes.Buffer
	fmt.Fprintf(&body, "tree %s\n", o.tree)
	for _, p := range o.parents {
		fmt.Fprintf(&body, "parent %s\n", p)
	}
	fmt.Fprintf(&body, "author %s %s\n", o.author, o.when)
	fmt.Fprintf(&body, "committer %s %s\n", o.author, o.when)
	fmt.Fprintf(&body, "\n%s\n", o.message)
	return b.store(Commit, body.Bytes())
}

func (b *builder) branch(name string, at Hash) {
	b.write("refs/heads/"+name, at.String()+"\n")
}

// reflog writes the local log of where a ref has pointed, which is the
// file that does not travel with a push and does travel with the folder.
func (b *builder) reflog(ref string, lines ...string) {
	b.t.Helper()
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(line + "\n")
	}
	b.write("logs/"+ref, out.String())
}

func logLine(from, to Hash, who, what string) string {
	return from.String() + " " + to.String() + " " + who + " 1725190000 +0200\t" + what
}

func (b *builder) open() *Repo {
	b.t.Helper()
	r, err := OpenRepo(b.work)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(r.Close)
	return r
}

// leaky is the repository the tests keep coming back to: a secret
// committed and deleted, a key still in the tree, a nested directory, and
// an object nothing points at.
func leaky(t *testing.T) *builder {
	t.Helper()
	b := newRepo(t)

	env := b.blob("STRIPE_KEY=" + "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc\n" +
		"DB=postgres://app:Tr0ub4dor3@db.internal.acme.corp/orders\n")
	readme := b.blob("# orders\n\nSee https://api.bigbank.internal/v2 for the rest.\n")
	key := b.blob("-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n")
	code := b.blob("package main\n\nfunc main() {}\n")

	first := b.commit(commitOpts{
		tree:    b.tree(map[string]Hash{".env": env, "README.md": readme}),
		message: "ACME-4417 first cut",
	})
	second := b.commit(commitOpts{
		tree: b.tree(map[string]Hash{
			"README.md":     readme,
			"deploy/id_rsa": key,
			"src/orders.go": code,
		}),
		parents: []Hash{first},
		author:  "Luis Marin <luismarin84@gmail.com>",
		message: "take the env file out",
	})
	b.branch("main", second)

	var nothing Hash
	ana := "Ana Ruiz <ana@acme.corp>"
	luis := "Luis Marin <luismarin84@gmail.com>"
	b.reflog("refs/heads/main",
		logLine(nothing, first, ana, "commit (initial): ACME-4417 first cut"),
		logLine(first, second, luis, "commit: take the env file out"))
	b.reflog("HEAD",
		logLine(nothing, first, ana, "commit (initial): ACME-4417 first cut"),
		logLine(first, second, luis, "commit: take the env file out"),
		logLine(second, first, luis, "reset: moving to HEAD~1"),
		logLine(first, second, luis, "commit (amend): take the env file out"))

	// An object on no branch: what an amended commit leaves behind.
	b.blob("PASSWORD=hunter2andmore\n")
	return b
}

func mustTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	at, err := time.Parse(layout, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
