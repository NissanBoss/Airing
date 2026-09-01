package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The one finding this tool exists for: a file taken out of the tree is
// still on the branch, and the push sends it.
func TestDeletedFileStillGoesOut(t *testing.T) {
	a := inspect(leaky(t).open())

	var gone *Placed
	for _, placed := range a.History.Gone() {
		if placed.Paths[".env"] {
			gone = placed
		}
	}
	if gone == nil {
		t.Fatal("the deleted .env is not in the history")
	}
	if gone.InHead {
		t.Error("it is not in the tree any more and the walk says it is")
	}
	if !a.History.Reachable[gone.Hash] {
		t.Error("it is reachable through the commit that added it, so a push sends it")
	}
}

func TestTheReportSaysItWasTakenOut(t *testing.T) {
	text := renderOf(t, leaky(t), false)
	for _, want := range []string{
		"2 secrets are in .env",
		"taken out later, and still on the branch",
		"1 file was taken out and goes out anyway",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report never says %q", want)
		}
	}
}

// The mode of a directory is written as octal with no padding, so it is
// 40000 and not 040000. Getting this wrong walks no subdirectory at all,
// and the only sign of it is a clone straight off a server reporting a
// fifth of itself as unreachable.
func TestSubdirectoriesAreWalked(t *testing.T) {
	a := inspect(leaky(t).open())
	if _, found := a.History.HeadPaths["deploy/id_rsa"]; !found {
		t.Fatal("nothing under a subdirectory was walked")
	}
	if _, found := a.History.HeadPaths["src/orders.go"]; !found {
		t.Error("the second subdirectory was missed")
	}
	for _, h := range a.History.HeadPaths {
		if !a.History.Reachable[h] {
			t.Error("a blob in the tree came out unreachable")
		}
	}
}

func TestIsTree(t *testing.T) {
	for _, mode := range []string{"40000", "040000"} {
		if !isTree(mode) {
			t.Errorf("%s is a directory", mode)
		}
	}
	for _, mode := range []string{"100644", "100755", "120000", "160000"} {
		if isTree(mode) {
			t.Errorf("%s is not a directory", mode)
		}
	}
}

// A submodule is a commit hash written into a tree and the object itself
// lives in another repository, so chasing it would report a gap in every
// repository that has one.
func TestSubmoduleEntryIsNotChased(t *testing.T) {
	b := newRepo(t)
	code := b.blob("x\n")

	var body bytes.Buffer
	sub := b.tree(map[string]Hash{"orders.go": code})
	body.WriteString("40000 src\x00")
	body.Write(sub[:])
	body.WriteString("160000 vendor\x00")
	body.Write(bytes.Repeat([]byte{0x11}, 20))
	tree := b.store(Tree, body.Bytes())

	b.branch("main", b.commit(commitOpts{tree: tree, message: "with a submodule"}))
	a := inspect(b.open())

	if len(a.Gaps) > 0 {
		t.Errorf("a submodule was read as a missing object: %v", a.Gaps)
	}
	if _, found := a.History.HeadPaths["vendor"]; found {
		t.Error("the submodule was counted as a file")
	}
}

func TestUnreachableObjectsStayInTheirOwnPile(t *testing.T) {
	a := inspect(leaky(t).open())
	if len(a.History.Loose) != 1 {
		t.Fatalf("expected the one object on no branch, got %d", len(a.History.Loose))
	}
	if len(a.Kept) != 1 {
		t.Fatalf("the secret in it was not found: %d", len(a.Kept))
	}
	for _, f := range a.Found {
		if f.Blob == a.History.Loose[0] {
			t.Error("an unreachable object was filed as something a push would send")
		}
	}

	text := renderOf(t, leaky(t), false)
	at := strings.Index(text, "on no branch")
	if at < 0 {
		t.Fatal("the report never mentions it")
	}
	if tag := lastTagBefore(text, at); tag != "HANDED " {
		t.Errorf("it was reported as %q and a push does not send it", tag)
	}
}

// Nothing that would leak a secret may print, in either mode. This is the
// promise on the front of the README, and it is worth testing against the
// finished report rather than against the pieces.
func TestNoSecretIsEverPrinted(t *testing.T) {
	live := "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	for _, reveal := range []bool{false, true} {
		text := renderOf(t, leaky(t), reveal)
		for _, secret := range []string{live, "Tr0ub4dor3", "hunter2andmore", "b3BlbnNzaC1rZXktdjEAAAAA"} {
			if strings.Contains(text, secret) {
				t.Errorf("reveal=%v printed %q", reveal, secret)
			}
		}
	}
}

func TestRevealNamesPeopleAndNothingElse(t *testing.T) {
	quiet := renderOf(t, leaky(t), false)
	loud := renderOf(t, leaky(t), true)

	if strings.Contains(quiet, "ana@acme.corp") || strings.Contains(quiet, "Ana Ruiz") {
		t.Error("somebody was named without --reveal")
	}
	if !strings.Contains(quiet, "hidden:") {
		t.Error("nothing was covered over")
	}
	if !strings.Contains(loud, "Ana Ruiz") || !strings.Contains(loud, "acme.corp") {
		t.Error("--reveal did not name the people, which is what it is for")
	}
	if !strings.Contains(loud, "put there by") {
		t.Error("--reveal did not say who committed the secret")
	}
}

func TestFilesThatMustNotBePublished(t *testing.T) {
	text := renderOf(t, leaky(t), false)
	if !strings.Contains(text, "must not be published") || !strings.Contains(text, "deploy/id_rsa") {
		t.Error("a private key sitting in the tree was not reported")
	}
}

// Most .pem files are certificates and a certificate is public by design.
// Reporting every one of them is how a tool gets uninstalled.
func TestCertificateIsNotAPrivateKey(t *testing.T) {
	b := newRepo(t)
	cert := b.blob("-----BEGIN CERTIFICATE-----\nMIIBcertificatebody\n-----END CERTIFICATE-----\n")
	key := b.blob("-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{
		"certs/server.pem": cert,
		"certs/client.pem": key,
	}), message: "certificates"}))

	text := render(t, b.open(), false)
	if strings.Contains(text, "certs/server.pem") {
		t.Error("a certificate was reported as a private key")
	}
	if !strings.Contains(text, "certs/client.pem") {
		t.Error("a real private key with a .pem name was missed")
	}
}

func TestPrivateSubmoduleIsReported(t *testing.T) {
	b := newRepo(t)
	x := b.blob("x\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{"a.txt": x}), message: "one"}))
	modules := "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = git@git.acme.corp:platform/lib.git\n"
	if err := os.WriteFile(filepath.Join(b.work, ".gitmodules"), []byte(modules), 0o644); err != nil {
		t.Fatal(err)
	}

	loud := render(t, b.open(), true)
	if !strings.Contains(loud, "somewhere not public") || !strings.Contains(loud, "vendor/lib") {
		t.Error("a submodule on a private host was not reported")
	}
	quiet := render(t, b.open(), false)
	if strings.Contains(quiet, "git.acme.corp:platform/lib.git") {
		t.Error("the address printed without --reveal")
	}
}

func TestInternalHostnamesInTheFiles(t *testing.T) {
	text := renderOf(t, leaky(t), true)
	if !strings.Contains(text, "api.bigbank.internal") {
		t.Error("a hostname written into a file was missed")
	}
	if !strings.Contains(text, "db.internal.acme.corp") {
		t.Error("a hostname in a connection string was missed")
	}
}

func TestTicketNumbersInMessages(t *testing.T) {
	text := renderOf(t, leaky(t), true)
	if !strings.Contains(text, "ACME-4417") {
		t.Error("a ticket number in a commit message was missed")
	}
}

func TestPeopleAreCounted(t *testing.T) {
	a := inspect(leaky(t).open())
	if len(a.History.Commits) != 2 {
		t.Fatalf("expected two commits, got %d", len(a.History.Commits))
	}
	if !strings.Contains(renderOf(t, leaky(t), true), "2 people and 2 email domains") {
		t.Error("the two authors and their domains were not counted")
	}
}

// A clean repository has to come out clean, or nobody believes the rest.
func TestNothingWrongComesOutQuiet(t *testing.T) {
	b := newRepo(t)
	code := b.blob("package main\n\nfunc main() {}\n")
	doc := b.blob("# a project\n\nIt does a thing.\n")
	b.branch("main", b.commit(commitOpts{tree: b.tree(map[string]Hash{
		"main.go":   code,
		"README.md": doc,
	}), message: "first"}))

	a := inspect(b.open())
	if a.leaking() {
		t.Error("a clean repository was called leaky")
	}
	text := render(t, b.open(), false)
	if strings.Contains(text, "[PUBLIC ]") {
		t.Errorf("a clean repository got a loud finding:\n%s", text)
	}
	if !strings.Contains(text, "which is not the same as nothing being there") {
		t.Error("the report did not say what it had not looked at")
	}
}

func TestGitAsAFilePointingElsewhere(t *testing.T) {
	b := leaky(t)
	moved := filepath.Join(filepath.Dir(b.work), "somewhere-else")
	if err := os.Rename(b.git, moved); err != nil {
		t.Skipf("cannot move the git directory here: %v", err)
	}
	if err := os.WriteFile(b.git, []byte("gitdir: "+moved+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRepo(b.work)
	if err != nil {
		t.Fatalf("a worktree and a submodule both look like this: %v", err)
	}
	defer r.Close()
	if len(r.Refs) == 0 {
		t.Error("no refs were read through the pointer")
	}
}

func TestBareRepository(t *testing.T) {
	b := leaky(t)
	r, err := OpenRepo(b.git)
	if err != nil {
		t.Fatalf("a bare repository is the git directory handed over on its own: %v", err)
	}
	defer r.Close()
	if len(r.Refs) == 0 {
		t.Error("no refs in a bare repository")
	}
}

func TestNotARepository(t *testing.T) {
	if _, err := OpenRepo(t.TempDir()); err == nil {
		t.Error("an empty directory is not a repository")
	}
}

func render(t *testing.T, r *Repo, reveal bool) string {
	t.Helper()
	a := inspect(r)
	var out bytes.Buffer
	a.writeUp(reveal).render(&out, "test  ("+a.summary()+")")
	return out.String()
}

func renderOf(t *testing.T, b *builder, reveal bool) string {
	t.Helper()
	return render(t, b.open(), reveal)
}

// lastTagBefore says which finding a piece of text landed in, which is the
// only way to test that the two piles never get mixed.
func lastTagBefore(text string, at int) string {
	head := text[:at]
	open := strings.LastIndex(head, "    [")
	if open < 0 || open+12 > len(head) {
		return ""
	}
	return head[open+5 : open+12]
}

func renderReport(r *Report) string {
	var out bytes.Buffer
	r.render(&out, "test")
	return out.String()
}
