# Airing

What your repository releases when it goes public.

Giving something an airing is hanging it out where the whole street can see
it. Making a repository public is the same, and most people press the button
knowing only what is in the folder.

```
    [PUBLIC ] 2 secrets are in .env
             a Stripe live key                 taken out later, and still on the branch
             a password in a connection string taken out later, and still on the branch

    [PUBLIC ] 1 file was taken out and goes out anyway
             .env                              an environment file, added 4 March 2026
               the file the application reads its secrets out of

    [PUBLIC ] 1 file is here that must not be published
             deploy/id_rsa                     a private ssh key
               whoever clones it can log in wherever that key is trusted

    [HANDED ] 6 objects are in the store and on no branch
             6 objects                         reachable from nothing

    [HANDED ] the reflog remembers what the branches used to be
             23 lines                          in the local log
             3 resets and amends               where something was undone

    [TELLS  ] 2 people and 2 email domains in the history
             hidden:64fde26e                   4 commits, Nov 2025 to Mar 2026
             hidden:c1a17a5d                   6 commits, Jan 2026 to Mar 2026

    [TELLS  ] 3 internal hostnames are written into the files
             hidden:2f81aa04                   in src/bank.py
```

## The problem

**Publishing a repository does not publish the folder. It publishes the
history.**

A push sends everything reachable from the refs it pushes: every commit on
every branch and tag, every tree those commits point at, and every blob in
those trees. The file somebody committed on the Tuesday and deleted on the
Wednesday is reachable through the commit that added it, so it goes, and one
command brings it back. Deleting a file, committing the deletion, and looking
at a clean folder is the most common way a live credential gets published,
because everything on the screen says it is gone.

The same push sends every name and address that has ever authored a commit,
the hours of the day those commits were made, the branch names with the
customer in them and the ticket numbers in the messages. None of that is in
the folder either.

**And half of what worries people is not published at all.** The reflog, the
stash, the config with the remote URLs, and the objects nothing points at any
more: none of it travels with a push. All of it travels the moment somebody
copies the folder, hands over a zip, or shares a drive. That is a different
decision on a different day, and a report that mixes the two is a report
nobody can act on.

So this sorts everything into those two piles and never mixes them.

## What already exists

[gitleaks](https://github.com/gitleaks/gitleaks) is the standard answer, with
27,000 stars and a good reason for every one of them. It is a secret scanner:
it walks the history looking for credentials, and it is very good at that.
It has nothing to say about the deleted file that is not a credential, the
authors, the email domains, the branch names, the reflog, the stash, or the
objects on no branch, and it does not distinguish what a push sends from what
only goes with the folder, because that is not the question it was built to
answer. It also prints what it finds.

[trufflehog](https://github.com/trufflesecurity/trufflehog) is the same
category and goes further: it verifies a found credential by calling the
provider. That is genuinely useful and it means the tool talks to the
network, which is a thing to know before pointing it at a private repository.

[git-filter-repo](https://github.com/newren/git-filter-repo) and
[BFG](https://rtyley.github.io/bfg-repo-cleaner/) are the other half of the
shelf, and they are rewriters. They are what you reach for once you already
know what has to come out.

Nothing tells you what the whole repository is about to release, in one
answer, before you decide. That is the gap this is for.

## What it looks at

**The files that were taken out and go out anyway.** The headline. Every blob
in the history that is not in the tree any more, with the ones that are
credentials pulled out and named and the ordinary churn counted rather than
listed.

**Secrets, in what the push sends and in what it does not**, kept apart. A
key in a blob on the branch is published the moment the button is pressed. A
key in an object nothing points at is on the machine and stays there, unless
somebody copies the folder, and telling people otherwise is how a report gets
ignored.

**The files that must not be published at all**: private keys, environment
files, credential stores, the ones that are in the tree right now and need no
digging to find.

**Who wrote it.** Every author and committer, every address, the email
domains they tie the repository to, and the hours of the day the commits
cluster into, which gives away the timezone whatever a profile says.

**The names and the messages.** Ticket numbers, internal hostnames, the
branch called `feature/bigbank-migration`. A commit message is not code and
nobody reads them back before publishing.

**The hostnames written into the files.** A name nothing outside can resolve
is a description of the network on the other side.

**The reflog, the stash, the config and the unreachable objects**, all in
their own pile, all labelled as what only travels with the folder.

**The weight.** A blob over five megabytes that is not in the tree any more
is the worst of both: nobody gets any use out of it and every clone downloads
it.

It reads the object store itself, loose objects and packfiles and delta
chains and all, so it does not need git installed and it does not depend on
git being willing to show it.

## What it does not do

It does not fix anything. There is no `--clean`, no rewrite, no filter. It
reads, it says what is there, and what to do about it is a decision with
consequences that belongs to somebody who understands the repository.

It reads text blobs under four megabytes. A key inside a compiled artefact,
inside a zip that was committed, or inside a submodule it cannot see, is
somewhere it never looked, and the report says so rather than implying a
clean bill.

The gitignore matching is deliberately partial. Negations, anchoring and the
double star are not implemented, because a wrong answer there produces one
quiet note and implementing the whole specification correctly is a week
nobody asked for.

It cannot know that `ACME-4417` is your tracker and `UTF-8` is not, beyond a
list of the standards that have the same shape.

## Running it

```
airing                     the repository you are standing in
airing ../thing            somewhere else
airing --reveal            name the people and print the lines
airing --version
```

`--reveal` turns off the covering on names, addresses, hostnames and ticket
numbers, and adds the line each secret was found on and who committed it.
**It does not print secrets.** Nothing does. There is no flag for that, and a
test reads the source to keep it that way.

Exit codes, for a hook or a pipeline:

| Code | Meaning |
|---|---|
| 0 | nothing that goes out should not |
| 1 | something goes out that should not |
| 2 | it could not read the repository at all |
| 3 | it read some of it and says which part it could not |

## Building it

```
go build
```

Go 1.26 or newer, nothing else. No dependencies, and a test that fails the
build if one appears.

```
sh build.sh v1
```

builds the five packages the releases are made of.

## The promises

- **It only reads.** Nothing is written, moved, or deleted, in the repository
  or anywhere else. A test reads the source and fails if a write appears.
- **It never runs git.** The object store is parsed here. There is no
  subprocess.
- **No socket is opened.** No version check, no telemetry, no looking a token
  up to see whether it still works.
- **It never prints a secret**, with or without `--reveal`. Secrets are
  covered the moment the blob comes off the disk, so there is no later point
  at which the original could reach the screen.
- **It says what it could not read**, rather than counting silence as a pass.

## Licence

MIT.
