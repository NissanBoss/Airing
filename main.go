package main

// Airing says what a private repository releases the moment it goes public.
//
// Giving something an airing is hanging it out where the whole street can
// see it. Pressing the button on a repository is the same, and most people
// press it without knowing what goes with it.
//
// What goes is not the folder. It is every commit on every branch and every
// tag, and everything those commits ever pointed at, which includes the
// file somebody committed on the Tuesday and deleted on the Wednesday. It
// includes every name and address that has ever authored a commit, the
// hours they worked, the branch names with the customer in them and the
// ticket numbers in the messages.
//
// What does not go is the reflog, the stash, the config and the objects
// nothing points at any more. Those stay on the machine, and they travel
// the moment somebody copies the folder instead. The report keeps the two
// apart, because telling somebody their reflog is about to be published
// when it is not is a lie that costs the rest of it its credibility.
//
// It reads the git object store by hand, packfiles and delta chains and
// all. It never runs git, it never opens a socket, and it never writes.

import (
	"flag"
	"fmt"
	"os"
)

var version = "unreleased"

const (
	exitQuiet   = 0
	exitLeaking = 1 // something goes out that should not
	exitBroken  = 2
	exitPartial = 3 // something could not be read, so nothing here is a pass
)

func main() {
	reveal := flag.Bool("reveal", false,
		"name the people and print the lines. Never the secrets: those are never printed.")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("Airing " + version)
		return
	}

	dir := "."
	if flag.NArg() == 1 {
		dir = flag.Arg(0)
	} else if flag.NArg() > 1 {
		usage()
		os.Exit(exitBroken)
	}

	repo, err := OpenRepo(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "airing: %s: %s\n", where(dir, *reveal), plain(err))
		os.Exit(exitBroken)
	}
	defer repo.Close()

	found := inspect(repo)
	report := found.writeUp(*reveal)
	report.render(os.Stdout, where(dir, *reveal)+"  ("+found.summary()+")")

	switch {
	case report.worst() == Public:
		os.Exit(exitLeaking)
	case len(report.Gaps) > 0:
		os.Exit(exitPartial)
	}
	os.Exit(exitQuiet)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Airing - what a repository releases when it goes public

  airing                     the repository you are standing in
  airing ../thing            somewhere else
  airing --reveal            name the people and print the lines
  airing --version

Publishing a repository does not publish the folder. It publishes every
commit on every branch and tag, and everything those commits ever pointed
at, which is why the file somebody deleted last year goes out with it.

This says what goes, and separately what only goes if you hand over the
folder rather than pushing: the reflog, the stash, the config, and the
objects nothing points at any more.

It reads the object store itself, packfiles and delta chains included, so
it does not need git installed and it can see the parts git will not show
you without being asked exactly the right way.

It only reads. Nothing is written, no socket is opened, no other program is
run, and it never prints a secret, with or without --reveal.`)
}
