package main

// What gets hidden, and what deliberately does not.
//
// The paths in the repository are not secrets. They are the thing about to
// be published, so deploy/id_rsa prints in full: hiding the one part
// somebody needs in order to go and look would make the report useless and
// protect nothing.
//
// What gets hidden is the small amount that is about the people rather than
// about the code: the names and addresses in the history, the internal
// hostname, the ticket number, the address of the private submodule. Those
// become a short digest, so two mentions of the same thing still read as
// the same thing and the counts add up, without naming anybody in something
// that gets pasted into a chat. --reveal turns that off, because at some
// point you have to look.
//
// Secrets are in neither category. They are covered at the moment they come
// off the disk and there is no flag that brings them back.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func hidden(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return "hidden:" + hex.EncodeToString(sum[:])[:8]
}

func show(value string, reveal bool) string {
	if reveal {
		return value
	}
	return hidden(value)
}

// plain is an error without the path Go writes into it. A gap already says
// which file it is, in the repository's own terms; the rest of the path is
// this machine's, with the user name in it.
func plain(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// where is the folder being read, the way the heading shows it: the home
// folder as ~. The paths inside the repository are the thing about to be
// published and print in full, but the one it sits in on this machine is
// not, and in v1 the heading printed it whole, user name and all, the first
// time anybody passed a full path instead of a dot.
func where(dir string, reveal bool) string {
	if reveal {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	rel, err := filepath.Rel(home, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return dir
	}
	if rel == "." {
		return "~"
	}
	return "~/" + filepath.ToSlash(rel)
}
