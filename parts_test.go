package main

// The pieces on their own: the object format, the files around it, and the
// small patterns that decide what gets said.

import (
	"bytes"
	"crypto/sha1"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A delta is a base length, a result length, and then instructions: a copy
// takes a run out of the base, an insert carries its own bytes. Getting
// either of them wrong gives an object that does not hash back to its own
// name, which is the check the store already makes.
func TestDeltaCopyAndInsert(t *testing.T) {
	base := []byte("the quick brown fox jumps over the lazy dog")

	var d bytes.Buffer
	d.WriteByte(byte(len(base)))
	d.WriteByte(19) // "the quick brown fox"
	d.WriteByte(0x91)
	d.WriteByte(0) // from the start
	d.WriteByte(19)

	out, err := apply(base, d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "the quick brown fox" {
		t.Errorf("copy gave %q", out)
	}

	d.Reset()
	d.WriteByte(byte(len(base)))
	d.WriteByte(9)
	d.WriteByte(9)
	d.WriteString("a new dog")
	if out, err = apply(base, d.Bytes()); err != nil {
		t.Fatal(err)
	}
	if string(out) != "a new dog" {
		t.Errorf("insert gave %q", out)
	}
}

// A copy with its size left at zero means sixty four kilobytes, which is
// the one special case in the format and the one everybody misses.
func TestDeltaCopySizeZeroMeansSixtyFourK(t *testing.T) {
	base := bytes.Repeat([]byte("x"), 0x10000)

	var d bytes.Buffer
	d.Write(varintOut(len(base)))
	d.Write(varintOut(0x10000))
	d.WriteByte(0x80) // copy, no offset bytes, no size bytes

	out, err := apply(base, d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0x10000 {
		t.Errorf("expected 65536 bytes back, got %d", len(out))
	}
}

func TestDeltaRefusesToRunOffTheEnd(t *testing.T) {
	base := []byte("short")
	var d bytes.Buffer
	d.WriteByte(byte(len(base)))
	d.WriteByte(200)
	d.WriteByte(0x91)
	d.WriteByte(0)
	d.WriteByte(200) // more than the base has
	if _, err := apply(base, d.Bytes()); err == nil {
		t.Error("a copy past the end of the base was allowed")
	}
}

func varintOut(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func TestVarint(t *testing.T) {
	for _, want := range []int{0, 1, 127, 128, 300, 65536, 1 << 20} {
		got, used := varint(varintOut(want))
		if got != want {
			t.Errorf("%d came back as %d", want, got)
		}
		if used != len(varintOut(want)) {
			t.Errorf("%d used %d bytes", want, used)
		}
	}
}

func TestObjectVerify(t *testing.T) {
	body := []byte("hello\n")
	whole := append([]byte("blob 6\x00"), body...)
	o := Object{Kind: Blob, Body: body, Hash: Hash(sha1.Sum(whole))}
	if !o.Verify() {
		t.Error("an object that is its own hash did not verify")
	}
	o.Body = []byte("hellp\n")
	if o.Verify() {
		t.Error("a changed body still verified")
	}
}

func TestLooseObjectRoundTrip(t *testing.T) {
	b := newRepo(t)
	h := b.blob("some content\n")

	s, err := OpenStore(b.git)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	o, err := s.Read(h)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != Blob || string(o.Body) != "some content\n" {
		t.Errorf("read back %v %q", o.Kind, o.Body)
	}
	if !o.Verify() {
		t.Error("the object did not hash back to its own name")
	}
}

// An annotated tag is an object of its own that points at the commit, so
// the walk has to go through it or a tagged release looks unreachable.
func TestAnnotatedTagIsFollowed(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	commit := b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), message: "one"})
	b.branch("main", commit)

	tag := b.store(Tag, []byte("object "+commit.String()+"\ntype commit\ntag v1\n"+
		"tagger Ana Ruiz <ana@acme.corp> 1725190000 +0200\n\nversion one\n"))
	b.write("refs/tags/v1", tag.String()+"\n")

	a := inspect(b.open())
	if !a.History.Reachable[commit] {
		t.Error("the commit behind an annotated tag came out unreachable")
	}
	if len(a.History.Loose) != 0 {
		t.Errorf("%d objects were called unreachable and all of them are tagged", len(a.History.Loose))
	}
}

// Most of the branches in an old repository live in packed-refs rather
// than in files, and the lines beginning with a caret are the object a tag
// points at rather than refs of their own.
func TestPackedRefs(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	commit := b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), message: "one"})
	b.branch("main", commit)
	b.write("packed-refs", "# pack-refs with: peeled fully-peeled sorted \n"+
		commit.String()+" refs/heads/old-work\n"+
		commit.String()+" refs/tags/v1\n"+
		"^"+commit.String()+"\n")

	r := b.open()
	names := map[string]bool{}
	for _, ref := range r.Refs {
		names[ref.Name] = true
	}
	if !names["refs/heads/old-work"] {
		t.Error("a branch in packed-refs was not read")
	}
	if len(r.Refs) != 3 {
		t.Errorf("expected three refs, got %d: %v", len(r.Refs), names)
	}
}

// A loose ref and a packed one of the same name are the same branch, and
// the loose file is the newer of the two.
func TestLooseRefWinsOverPacked(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	first := b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), message: "one"})
	second := b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), parents: []Hash{first}, message: "two"})
	b.branch("main", second)
	b.write("packed-refs", first.String()+" refs/heads/main\n")

	r := b.open()
	if len(r.Refs) != 1 {
		t.Fatalf("the same branch was counted twice: %d", len(r.Refs))
	}
	if r.Refs[0].Hash != second {
		t.Error("the packed copy won and it is the older one")
	}
}

func TestReflogLine(t *testing.T) {
	line := "0000000000000000000000000000000000000000 " +
		"1111111111111111111111111111111111111111 " +
		"Ana Ruiz <ana@acme.corp> 1725190000 +0200\tcommit (initial): first"
	entry, ok := readLogLine(line)
	if !ok {
		t.Fatal("a reflog line did not parse")
	}
	if entry.Who != "Ana Ruiz <ana@acme.corp>" {
		t.Errorf("who came out as %q", entry.Who)
	}
	if entry.What != "commit (initial): first" {
		t.Errorf("what came out as %q", entry.What)
	}
	if entry.When.Hour() != 13 {
		t.Errorf("the hour came out as %d, and 11:26 UTC at +0200 is 13:26", entry.When.Hour())
	}
	if _, ok := readLogLine("not a reflog line"); ok {
		t.Error("anything at all parsed as a reflog line")
	}
}

// The offset is the point: it says where the person was sitting, and
// turning it into local time throws that away.
func TestTimeKeepsTheWritersOffset(t *testing.T) {
	at := whenFrom("1725190000", "+0530")
	if _, offset := at.Zone(); offset != 5*3600+30*60 {
		t.Errorf("the offset came out as %d seconds", offset)
	}
	if whenFrom("not a number", "+0200").IsZero() != true {
		t.Error("a broken timestamp should come back as nothing")
	}
}

func TestReadPerson(t *testing.T) {
	p := readPerson("Ana Ruiz <ana@acme.corp> 1725190000 +0200")
	if p.Name != "Ana Ruiz" || p.Email != "ana@acme.corp" {
		t.Errorf("got %q and %q", p.Name, p.Email)
	}
	if readPerson("Nobody").Name != "Nobody" {
		t.Error("a line with no address should still give a name")
	}
}

func TestReadTree(t *testing.T) {
	var body bytes.Buffer
	body.WriteString("100644 README.md\x00")
	body.Write(bytes.Repeat([]byte{0xab}, 20))
	body.WriteString("40000 src\x00")
	body.Write(bytes.Repeat([]byte{0xcd}, 20))

	entries := readTree(body.Bytes())
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].Name != "README.md" || entries[0].Mode != "100644" {
		t.Errorf("first entry is %+v", entries[0])
	}
	if !isTree(entries[1].Mode) {
		t.Error("the directory did not read as one")
	}
	if len(readTree([]byte("100644 truncated"))) != 0 {
		t.Error("a truncated tree should give nothing rather than a panic")
	}
}

func TestConfigRemotes(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), message: "one"}))
	b.write("config", "[core]\n\tbare = false\n"+
		"[remote \"origin\"]\n\turl = git@git.acme.corp:platform/orders.git\n\tfetch = +refs/heads/*\n"+
		"[remote \"public\"]\n\turl = https://github.com/somebody/orders.git\n"+
		"[branch \"main\"]\n\tremote = origin\n")

	r := b.open()
	if len(r.Remotes) != 2 {
		t.Fatalf("expected two remotes, got %d", len(r.Remotes))
	}
	if r.Remotes[0].Name != "origin" || !strings.Contains(r.Remotes[0].URL, "git.acme.corp") {
		t.Errorf("the first remote is %+v", r.Remotes[0])
	}

	text := render(t, b.open(), true)
	if !strings.Contains(text, "origin") {
		t.Error("a remote on a private host was not mentioned")
	}
	if strings.Contains(text, "github.com/somebody/orders.git") {
		t.Error("a public remote was reported and there is nothing to say about it")
	}
}

// The tracked file that matches a gitignore rule: the list looks like
// protection and is doing nothing, because ignoring only stops a file
// being added and this one is already in.
func TestTrackedFileMatchingTheIgnoreList(t *testing.T) {
	b := newRepo(t)
	log := b.blob("2026-09-01 started\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{"logs/run.log": log}), message: "one"}))
	if err := os.WriteFile(filepath.Join(b.work, ".gitignore"), []byte("*.log\n# a comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	text := render(t, b.open(), false)
	if !strings.Contains(text, "a rule in the gitignore") || !strings.Contains(text, "logs/run.log") {
		t.Error("a tracked file matching an ignore rule was not reported")
	}
}

func TestMatchesIgnore(t *testing.T) {
	yes := [][2]string{
		{"*.log", "logs/run.log"},
		{"node_modules/", "node_modules/left-pad/index.js"},
		{".env", ".env"},
		{"build", "build/output/app"},
		{"/dist", "dist/index.js"},
	}
	for _, pair := range yes {
		if !matchesIgnore(pair[0], pair[1]) {
			t.Errorf("%q should match %q", pair[0], pair[1])
		}
	}
	no := [][2]string{
		{"*.log", "logs/run.txt"},
		{"dist", "src/index.js"},
		{"", "anything"},
	}
	for _, pair := range no {
		if matchesIgnore(pair[0], pair[1]) {
			t.Errorf("%q should not match %q", pair[0], pair[1])
		}
	}
}

func TestPrivateLooking(t *testing.T) {
	private := []string{
		"git@git.acme.corp:platform/lib.git",
		"https://gitlab.acme.corp/platform/lib.git",
		"ssh://git@10.0.0.4/srv/git/lib.git",
	}
	for _, url := range private {
		if !privateLooking(url) {
			t.Errorf("%q is not somewhere the public can follow", url)
		}
	}
	public := []string{
		"https://github.com/NissanBoss/Airing.git",
		"git@github.com:NissanBoss/Airing.git",
		"https://gitlab.com/somebody/thing.git",
		"",
	}
	for _, url := range public {
		if privateLooking(url) {
			t.Errorf("%q is public", url)
		}
	}
}

// The loose pattern matches this.internal, which is a property access in
// half the JavaScript ever written. Inside a file the name has to sit
// where a host goes.
func TestInternalHostInsideAFile(t *testing.T) {
	yes := []string{
		`API = "https://api.bigbank.internal/v2"`,
		"DB=postgres://app@db.internal.acme.corp/orders",
		"host: build.acme.corp",
		`hosts = ["one.acme.lan", "two.acme.lan"]`,
		"deploy@releases.acme.intranet",
	}
	for _, line := range yes {
		if !insideFile.MatchString(line) {
			t.Errorf("missed the host in %q", line)
		}
	}
	no := []string{
		"return this.internal;",
		"if (config.corp && other.lan) {",
		"const x = state.internal.value",
		"import helper from './utils.internal'",
		"self.internal_name = 3",
		"printer.local.queue()",
	}
	for _, line := range no {
		if insideFile.MatchString(line) {
			t.Errorf("%q is code and it was read as a hostname", line)
		}
	}
}

func TestTicketPattern(t *testing.T) {
	for _, s := range []string{"ACME-4417 fix the thing", "closes PLATFORM-12"} {
		if !ticket.MatchString(s) {
			t.Errorf("missed the ticket in %q", s)
		}
	}
	for _, s := range []string{"a-1 is not a ticket", "no ticket here"} {
		if ticket.MatchString(s) {
			t.Errorf("%q is not a ticket", s)
		}
	}
}

// Somebody who works evenings has their quiet hours in the middle of the
// list, and reading it flat says midnight to eleven, which says nothing.
func TestAwakeWrapsRoundMidnight(t *testing.T) {
	var hours [24]int
	total := 0
	for _, hour := range []int{21, 22, 23, 0, 1, 2} {
		hours[hour] = 10
		total += 10
	}
	from, to, ok := awake(hours, total)
	if !ok {
		t.Fatal("a clear evening pattern was not found")
	}
	if from != 21 || to != 2 {
		t.Errorf("got %02d:00 to %02d:59", from, to)
	}

	var flat [24]int
	for hour := range flat {
		flat[hour] = 5
	}
	if _, _, ok := awake(flat, 120); ok {
		t.Error("a flat day says nothing about anybody and should not be reported")
	}
	if _, _, ok := awake(hours, 4); ok {
		t.Error("four commits are not enough to say when somebody works")
	}
}

func TestHidden(t *testing.T) {
	if hidden("ana@acme.corp") != hidden("ANA@ACME.CORP") {
		t.Error("the same address twice should read as the same thing")
	}
	if hidden("ana@acme.corp") == hidden("luis@acme.corp") {
		t.Error("two people came out as one")
	}
	if hidden("  ") != "" {
		t.Error("nothing should cover to nothing")
	}
	if strings.Contains(hidden("ana@acme.corp"), "acme") {
		t.Error("the cover has the original in it")
	}
	if show("ana@acme.corp", true) != "ana@acme.corp" {
		t.Error("--reveal is meant to show it")
	}
}

func TestCarriesAndTemplates(t *testing.T) {
	kit, is := carries("config/deploy/id_rsa")
	if !is || !kit.Grave {
		t.Error("a private key under two directories was not recognised")
	}
	if _, is := carries("src/main.go"); is {
		t.Error("ordinary source was on the list")
	}
	for _, where := range []string{"test/fixtures/.env", "testdata/id_rsa", "examples/.env.local"} {
		if !isTemplate(where) {
			t.Errorf("%q is a fixture and reporting it is noise", where)
		}
	}
	if isTemplate("src/.env") {
		t.Error("a real environment file was taken for a fixture")
	}
}

func TestEveryNamedFileSaysWhy(t *testing.T) {
	for _, kit := range named() {
		if strings.TrimSpace(kit.Why) == "" {
			t.Errorf("%s has nothing to say about what somebody gets", kit.Pattern)
		}
		if strings.TrimSpace(kit.What) == "" {
			t.Errorf("%s does not say what it is", kit.Pattern)
		}
	}
}

func TestColumnsAndFolding(t *testing.T) {
	lines := columns("short", "a value")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "short") {
		t.Errorf("a short row wrapped: %q", lines)
	}
	long := columns(strings.Repeat("a", 50), "a value that has to go somewhere")
	if len(long) < 2 {
		t.Error("a path too long for its column should drop to the next line")
	}
	if strings.Contains(long[0], "a value") {
		t.Error("the value was pushed off the side instead of down")
	}
	for _, line := range folded("  ", strings.Repeat("word ", 40)) {
		if len(line) > wrap {
			t.Errorf("a folded line is %d characters", len(line))
		}
	}
}

func TestAtMostAndPlural(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	kept := atMost(lines, 10)
	if len(kept) != 11 || !strings.Contains(kept[10], "20 more") {
		t.Errorf("atMost gave %d lines ending %q", len(kept), kept[len(kept)-1])
	}
	if plural(1, "object", "objects") != "1 object" {
		t.Error("one object is not objects")
	}
	if plural(0, "object", "objects") != "0 objects" {
		t.Error("none of them is still objects")
	}
}

func TestSize(t *testing.T) {
	for _, pair := range [][2]string{
		{size(512), "512 bytes"},
		{size(2048), "2.0 kB"},
		{size(5 << 20), "5.0 MB"},
		{size(3 << 30), "3.0 GB"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("got %q, wanted %q", pair[0], pair[1])
		}
	}
}

func TestStandardsAreNotTickets(t *testing.T) {
	for _, s := range []string{"ACME-4417", "PLATFORM-12", "OPS-7"} {
		if !isTicket(s) {
			t.Errorf("%q is a ticket", s)
		}
	}
	for _, s := range []string{"UTF-8", "SHA-256", "RFC-2119", "TLS-1", "CVE-2024", "P-256"} {
		if isTicket(s) {
			t.Errorf("%q is a standard and reporting it is noise", s)
		}
	}
}

// The stash is on no branch, so a push does not send it, and it is where
// the half finished experiment with the real credentials tends to end up.
func TestStashIsFoundAndNotPushed(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	tree := b.tree(map[string]Hash{"a.txt": x})
	main := b.commit(commitOpts{tree: tree, message: "one"})
	b.branch("main", main)

	work := b.blob("PASSWORD=hunter2andmore\n")
	stashed := b.commit(commitOpts{
		tree:    b.tree(map[string]Hash{"a.txt": x, "notes.txt": work}),
		parents: []Hash{main},
		message: "WIP on main: trying the real credentials",
	})
	b.write("refs/stash", stashed.String()+"\n")
	b.reflog("refs/stash", logLine(main, stashed, "Ana Ruiz <ana@acme.corp>",
		"On main: trying the real credentials"))

	a := inspect(b.open())
	if len(a.Repo.Stash) == 0 {
		t.Fatal("the stash was not found")
	}

	text := render(t, b.open(), false)
	at := strings.Index(text, "in the stash")
	if at < 0 {
		t.Fatal("the stash is not in the report")
	}
	if tag := lastTagBefore(text, at); tag != "HANDED " {
		t.Errorf("the stash came out as %q and a push does not send it", tag)
	}
	if strings.Contains(text, "hunter2andmore") {
		t.Error("the report printed what was in the stash")
	}
}

func TestLooksLikeATest(t *testing.T) {
	yes := []string{
		"airing_test.go", "src/scan_test.go", "test_users.py", "conftest.py",
		"app/user.test.tsx", "app/user.spec.ts", "spec/models/user_spec.rb",
		"src/OrdersTest.java", "Orders/OrdersTests.cs",
	}
	for _, where := range yes {
		if !looksLikeATest(where) {
			t.Errorf("%q is a test", where)
		}
	}
	no := []string{
		"src/main.go", "latest.java", "protest.py", "contest.rb",
		"src/testing.go", "deploy/id_rsa",
	}
	for _, where := range no {
		if looksLikeATest(where) {
			t.Errorf("%q is not a test", where)
		}
	}
}

// A repository of any size has fixtures with made up tokens in them. One
// finding each is how a report stops being read, and dropping them is how
// the test that talks to something real goes out with a working key.
func TestFixturesAreGroupedAndNotDropped(t *testing.T) {
	b := newRepo(t)
	live := "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	one := b.blob("func TestPay(t *testing.T) { key := \"" + live + "\" }\n")
	two := b.blob("KEY = \"" + live + "\"\nDB = \"postgres://app:Tr0ub4dor3@db.example.com/x\"\n")
	real := b.blob("STRIPE_KEY=" + live + "\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{
		"pay_test.go": one,
		"test_pay.py": two,
		"config/.env": real,
	}), message: "one"}))

	text := render(t, b.open(), false)
	if !strings.Contains(text, "3 secrets are in files that look like tests") {
		t.Errorf("the fixtures were not grouped:\n%s", text)
	}
	if !strings.Contains(text, "pay_test.go") || !strings.Contains(text, "test_pay.py") {
		t.Error("the fixtures were dropped instead of grouped")
	}
	if !strings.Contains(text, "1 secret is in config/.env") {
		t.Error("the real one was swept up with the fixtures")
	}
}
