# Changelog

## v1.1

- **What a login form tells the browser is not a password.** A page that
  wrote `autocomplete = registering ? "new-password" : "current-password"`
  was reported as a password set in a file, once for every version of the
  form in the history: eleven times in the first public repository it was
  pointed at, none of them a secret. `current-password`, `new-password` and
  `one-time-code`, the values the HTML standard gives a form, are now among
  the words that stand in for a secret rather than being one.

## v1

The first one.

Airing says what a repository releases when it goes public, and it keeps two
answers apart that everything else runs together:

- **What a push sends.** Everything reachable from the branches and the tags,
  which includes every file anybody ever deleted, every author and address in
  the history, the hours those commits were made, the ticket numbers in the
  messages and the hostnames written into the files.
- **What only travels with the folder.** The reflog, the stash, the config
  with the remote URLs, and the objects nothing points at any more. None of
  it gets pushed, all of it gets copied, and saying otherwise would cost the
  rest of the report its credibility.

It reads the object store by hand: loose objects, packfiles, the index, and
delta chains both ways round. It does not need git installed, it never runs
git, it never opens a socket, it never writes, and it never prints a secret,
with or without `--reveal`.

Five packages: Windows, Linux, Linux on ARM, and both kinds of Mac. One
binary each, no dependencies, MIT.
