package main

// The git object store, read by hand.
//
// Nothing here shells out to git, and that is a decision rather than
// stubbornness. Two reasons. The first is that the whole point of this tool
// is to see what is in the repository including the parts git will not show
// you without being asked exactly the right way, and the second is that a
// single binary that needs git installed is not a single binary.
//
// So this is the format, implemented: loose objects, which are easy, and
// packfiles, which are not. A packfile stores most objects as a delta
// against another object, which may itself be a delta, so reading one blob
// can mean walking a chain and rebuilding each link. It is the most
// satisfying thing in here and the part most likely to be subtly wrong, so
// it is also the part with the most tests behind it.

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Kind int

const (
	Bad Kind = iota
	Commit
	Tree
	Blob
	Tag
	// The two that are not really kinds: a delta says "like that other one,
	// but with these changes", and the kind is whatever the base turns out
	// to be.
	offsetDelta = 6
	refDelta    = 7
)

func (k Kind) String() string {
	switch k {
	case Commit:
		return "commit"
	case Tree:
		return "tree"
	case Blob:
		return "blob"
	case Tag:
		return "tag"
	}
	return "unknown"
}

func kindOf(name string) Kind {
	switch name {
	case "commit":
		return Commit
	case "tree":
		return Tree
	case "blob":
		return Blob
	case "tag":
		return Tag
	}
	return Bad
}

// Hash is a git object name. Kept as an array rather than a string so that
// the maps this program lives in stay cheap: a big repository has hundreds
// of thousands of these.
type Hash [20]byte

func (h Hash) String() string { return hex.EncodeToString(h[:]) }
func (h Hash) Short() string  { return hex.EncodeToString(h[:])[:8] }
func (h Hash) IsZero() bool   { return h == Hash{} }
func hashFrom(s string) (Hash, bool) {
	var h Hash
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 20 {
		return h, false
	}
	copy(h[:], raw)
	return h, true
}

type Object struct {
	Hash Hash
	Kind Kind
	Body []byte
}

// Store is every object in the repository, from wherever it is kept.
type Store struct {
	Root  string
	packs []*pack

	// loose is every loose object found, so that listing everything does
	// not mean walking the directory again.
	loose map[Hash]string

	// cache holds recently rebuilt objects, because delta chains hit the
	// same bases over and over and rebuilding one from scratch each time
	// turns a second into a minute.
	cache map[Hash]Object
	order []Hash

	Gaps []string
}

// howManyCached is a few thousand objects, which is enough to keep the hot
// bases of a delta chain around and small enough not to hold a repository
// in memory.
const howManyCached = 4096

func OpenStore(gitDir string) (*Store, error) {
	s := &Store{
		Root:  gitDir,
		loose: map[Hash]string{},
		cache: map[Hash]Object{},
	}
	if err := s.findLoose(); err != nil {
		return nil, err
	}
	if err := s.findPacks(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) findLoose() error {
	root := filepath.Join(s.Root, "objects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // No objects directory at all is somebody else's error.
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) != 2 {
			continue
		}
		inner, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			s.Gaps = append(s.Gaps, "objects/"+name+": "+plain(err))
			continue
		}
		for _, f := range inner {
			if len(f.Name()) != 38 {
				continue
			}
			if h, ok := hashFrom(name + f.Name()); ok {
				s.loose[h] = filepath.Join(root, name, f.Name())
			}
		}
	}
	return nil
}

func (s *Store) findPacks() error {
	dir := filepath.Join(s.Root, "objects", "pack")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".idx") {
			continue
		}
		p, err := openPack(filepath.Join(dir, e.Name()))
		if err != nil {
			s.Gaps = append(s.Gaps, e.Name()+": "+plain(err))
			continue
		}
		s.packs = append(s.packs, p)
	}
	return nil
}

func (s *Store) Close() {
	for _, p := range s.packs {
		p.Close()
	}
}

// Everything lists every object the repository holds, packed or loose.
func (s *Store) Everything() []Hash {
	seen := make(map[Hash]bool, len(s.loose))
	out := make([]Hash, 0, len(s.loose))
	for h := range s.loose {
		seen[h] = true
		out = append(out, h)
	}
	for _, p := range s.packs {
		for _, h := range p.Hashes() {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

func (s *Store) Has(h Hash) bool {
	if _, found := s.loose[h]; found {
		return true
	}
	for _, p := range s.packs {
		if _, found := p.offsetOf(h); found {
			return true
		}
	}
	return false
}

// Read gives back one object, rebuilding it from its delta chain if that is
// how it is stored.
func (s *Store) Read(h Hash) (Object, error) {
	if o, found := s.cache[h]; found {
		return o, nil
	}
	o, err := s.read(h, 0)
	if err != nil {
		return Object{}, err
	}
	s.remember(o)
	return o, nil
}

// deepestChain is a guard rather than a limit anybody should hit. Git keeps
// delta chains under fifty by default; a thousand means something is
// pointing at itself and the alternative to stopping is a stack overflow.
const deepestChain = 1000

func (s *Store) read(h Hash, depth int) (Object, error) {
	if depth > deepestChain {
		return Object{}, fmt.Errorf("the delta chain for %s does not end", h.Short())
	}
	if o, found := s.cache[h]; found {
		return o, nil
	}
	if path, found := s.loose[h]; found {
		return readLoose(h, path)
	}
	for _, p := range s.packs {
		at, found := p.offsetOf(h)
		if !found {
			continue
		}
		return s.readAt(p, at, h, depth)
	}
	return Object{}, fmt.Errorf("%s is not in this repository", h.Short())
}

func (s *Store) remember(o Object) {
	if len(o.Body) > 1<<20 {
		// A big blob would push everything useful out of the cache to hold
		// one thing that is probably read once.
		return
	}
	if len(s.order) >= howManyCached {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	s.cache[o.Hash] = o
	s.order = append(s.order, o.Hash)
}

// readLoose reads the easy kind: zlib, then "<type> <size>\0" and the body.
func readLoose(h Hash, path string) (Object, error) {
	file, err := os.Open(path)
	if err != nil {
		return Object{}, err
	}
	defer file.Close()

	unzip, err := zlib.NewReader(file)
	if err != nil {
		return Object{}, err
	}
	defer unzip.Close()

	body, err := io.ReadAll(unzip)
	if err != nil {
		return Object{}, err
	}
	cut := bytes.IndexByte(body, 0)
	if cut < 0 {
		return Object{}, errors.New("no header in the object")
	}
	kind, size, found := strings.Cut(string(body[:cut]), " ")
	if !found {
		return Object{}, errors.New("the header does not say what it is")
	}
	if n, err := strconv.Atoi(size); err == nil && n != len(body)-cut-1 {
		return Object{}, fmt.Errorf("the header says %d bytes and there are %d",
			n, len(body)-cut-1)
	}
	return Object{Hash: h, Kind: kindOf(kind), Body: body[cut+1:]}, nil
}

// readAt pulls one object out of a pack at a known offset.
//
// This is where the format earns its reputation. The header is a variable
// length integer with the type buried in the first byte, and then the thing
// is either the object itself or a delta against another one, addressed
// either by how far back it sits in the same pack or by name.
func (s *Store) readAt(p *pack, at int64, h Hash, depth int) (Object, error) {
	kind, size, header, err := p.headerAt(at)
	if err != nil {
		return Object{}, err
	}

	switch kind {
	case offsetDelta:
		back, used, err := p.backwardsFrom(at + header)
		if err != nil {
			return Object{}, err
		}
		base, err := s.readAt(p, at-back, Hash{}, depth+1)
		if err != nil {
			return Object{}, err
		}
		patch, err := p.inflateAt(at+header+used, size)
		if err != nil {
			return Object{}, err
		}
		body, err := apply(base.Body, patch)
		if err != nil {
			return Object{}, err
		}
		return Object{Hash: h, Kind: base.Kind, Body: body}, nil

	case refDelta:
		name, err := p.hashAt(at + header)
		if err != nil {
			return Object{}, err
		}
		base, err := s.read(name, depth+1)
		if err != nil {
			return Object{}, err
		}
		patch, err := p.inflateAt(at+header+20, size)
		if err != nil {
			return Object{}, err
		}
		body, err := apply(base.Body, patch)
		if err != nil {
			return Object{}, err
		}
		return Object{Hash: h, Kind: base.Kind, Body: body}, nil

	default:
		body, err := p.inflateAt(at+header, size)
		if err != nil {
			return Object{}, err
		}
		return Object{Hash: h, Kind: Kind(kind), Body: body}, nil
	}
}

// apply rebuilds an object from a base and a delta.
//
// The delta is two sizes and then a list of instructions, each of which is
// either "copy this stretch of the base" or "insert these bytes". The copy
// instruction is the fiddly one: which of the offset and length bytes are
// present is given by the bits of the instruction byte itself, so the
// arguments are assembled a byte at a time depending on a bitmask, and a
// length of zero means sixty four kilobytes rather than nothing.
func apply(base, delta []byte) ([]byte, error) {
	at := 0
	baseSize, used := varint(delta[at:])
	at += used
	if baseSize != len(base) {
		return nil, fmt.Errorf("the delta expects a base of %d bytes and this one is %d",
			baseSize, len(base))
	}
	wantSize, used := varint(delta[at:])
	at += used

	out := make([]byte, 0, wantSize)
	for at < len(delta) {
		instruction := delta[at]
		at++

		if instruction == 0 {
			// Reserved, and in practice a sign the stream is not what it
			// claims to be.
			return nil, errors.New("a zero instruction in the delta")
		}

		if instruction&0x80 == 0 {
			// Insert: the low seven bits say how many bytes follow.
			n := int(instruction & 0x7f)
			if at+n > len(delta) {
				return nil, errors.New("the delta ends in the middle of an insert")
			}
			out = append(out, delta[at:at+n]...)
			at += n
			continue
		}

		// Copy: bits zero to three pick which bytes of the offset are
		// present, bits four to six the same for the length.
		offset, length := 0, 0
		for i := range 4 {
			if instruction&(1<<i) != 0 {
				if at >= len(delta) {
					return nil, errors.New("the delta ends in the middle of a copy")
				}
				offset |= int(delta[at]) << (8 * i)
				at++
			}
		}
		for i := range 3 {
			if instruction&(1<<(4+i)) != 0 {
				if at >= len(delta) {
					return nil, errors.New("the delta ends in the middle of a copy")
				}
				length |= int(delta[at]) << (8 * i)
				at++
			}
		}
		if length == 0 {
			length = 0x10000
		}
		if offset+length > len(base) || offset < 0 || length < 0 {
			return nil, fmt.Errorf("the delta copies %d bytes from %d of a %d byte base",
				length, offset, len(base))
		}
		out = append(out, base[offset:offset+length]...)
	}

	if len(out) != wantSize {
		return nil, fmt.Errorf("the delta said %d bytes and produced %d", wantSize, len(out))
	}
	return out, nil
}

// varint is git's little endian, seven bits at a time, high bit means more.
func varint(b []byte) (int, int) {
	value, shift, at := 0, 0, 0
	for at < len(b) {
		c := b[at]
		at++
		value |= int(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			break
		}
	}
	return value, at
}

// Verify checks an object really is what its name says it is, which is the
// one thing a hand written reader should not take on trust.
func (o Object) Verify() bool {
	if o.Hash.IsZero() {
		return true
	}
	sum := sha1.New()
	fmt.Fprintf(sum, "%s %d", o.Kind, len(o.Body))
	sum.Write([]byte{0})
	sum.Write(o.Body)
	var got Hash
	copy(got[:], sum.Sum(nil))
	return got == o.Hash
}

// pack is one packfile and its index.
type pack struct {
	name  string
	data  *os.File
	size  int64
	names []Hash
	// where[i] is the offset in the pack of names[i].
	where []int64
}

func openPack(idxPath string) (*pack, error) {
	idx, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, err
	}
	packPath := strings.TrimSuffix(idxPath, ".idx") + ".pack"
	data, err := os.Open(packPath)
	if err != nil {
		return nil, err
	}
	info, err := data.Stat()
	if err != nil {
		data.Close()
		return nil, err
	}

	p := &pack{name: filepath.Base(packPath), data: data, size: info.Size()}
	if err := p.readIndex(idx); err != nil {
		data.Close()
		return nil, err
	}
	if err := p.checkHeader(); err != nil {
		data.Close()
		return nil, err
	}
	return p, nil
}

func (p *pack) Close()         { p.data.Close() }
func (p *pack) Hashes() []Hash { return p.names }

func (p *pack) checkHeader() error {
	head := make([]byte, 12)
	if _, err := p.data.ReadAt(head, 0); err != nil {
		return err
	}
	if string(head[:4]) != "PACK" {
		return errors.New("this does not start with PACK")
	}
	if v := binary.BigEndian.Uint32(head[4:8]); v != 2 && v != 3 {
		return fmt.Errorf("pack version %d, and only 2 and 3 are understood", v)
	}
	return nil
}

// readIndex reads the version 2 index: a fanout table by first byte, then
// the names in order, then the checksums, then the offsets.
//
// The fanout is not used for searching here because the names are already
// sorted and a binary search over them is the same thing without the extra
// bookkeeping. It is still read, because its last entry is the only place
// the object count is written down.
func (p *pack) readIndex(idx []byte) error {
	if len(idx) < 8 {
		return errors.New("the index is too short to be one")
	}
	if string(idx[:4]) != "\377tOc" {
		return errors.New("this is a version 1 index, which nothing has written since 2007")
	}
	if v := binary.BigEndian.Uint32(idx[4:8]); v != 2 {
		return fmt.Errorf("index version %d, and only 2 is understood", v)
	}

	const fanout = 8 + 256*4
	if len(idx) < fanout {
		return errors.New("the index stops inside its fanout table")
	}
	count := int(binary.BigEndian.Uint32(idx[fanout-4 : fanout]))

	need := fanout + count*20 + count*4 + count*4
	if len(idx) < need {
		return fmt.Errorf("the index says %d objects and is too short for them", count)
	}

	p.names = make([]Hash, count)
	for i := range count {
		copy(p.names[i][:], idx[fanout+i*20:fanout+i*20+20])
	}

	smallAt := fanout + count*20 + count*4
	bigAt := smallAt + count*4
	p.where = make([]int64, count)
	for i := range count {
		small := binary.BigEndian.Uint32(idx[smallAt+i*4 : smallAt+i*4+4])
		if small&0x80000000 == 0 {
			p.where[i] = int64(small)
			continue
		}
		// The high bit means the real offset is over two gigabytes and
		// lives in the table after this one, which only appears in packs
		// that big.
		which := int(small & 0x7fffffff)
		if bigAt+which*8+8 > len(idx) {
			return errors.New("the index points past the end of its own large offset table")
		}
		p.where[i] = int64(binary.BigEndian.Uint64(idx[bigAt+which*8 : bigAt+which*8+8]))
	}
	return nil
}

// offsetOf finds an object by binary search, because the index keeps the
// names sorted.
func (p *pack) offsetOf(h Hash) (int64, bool) {
	low, high := 0, len(p.names)-1
	for low <= high {
		middle := (low + high) / 2
		switch bytes.Compare(p.names[middle][:], h[:]) {
		case 0:
			return p.where[middle], true
		case -1:
			low = middle + 1
		default:
			high = middle - 1
		}
	}
	return 0, false
}

// headerAt reads the type and size at an offset and says how many bytes it
// took. The first byte holds the type in the middle of it and the low four
// bits of the size; every byte after that adds seven more bits.
func (p *pack) headerAt(at int64) (kind int, size int, used int64, err error) {
	buf := make([]byte, 32)
	n, err := p.data.ReadAt(buf, at)
	if n == 0 && err != nil {
		return 0, 0, 0, err
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return 0, 0, 0, errors.New("nothing at that offset")
	}

	first := buf[0]
	kind = int(first>>4) & 7
	size = int(first & 0x0f)
	shift := 4
	used = 1
	for first&0x80 != 0 {
		if int(used) >= len(buf) {
			return 0, 0, 0, errors.New("the object header does not end")
		}
		first = buf[used]
		size |= int(first&0x7f) << shift
		shift += 7
		used++
	}
	return kind, size, used, nil
}

// backwardsFrom reads the distance an offset delta points back by, which
// has an encoding of its own and not the same one as everything else.
func (p *pack) backwardsFrom(at int64) (int64, int64, error) {
	buf := make([]byte, 16)
	n, err := p.data.ReadAt(buf, at)
	if n == 0 && err != nil {
		return 0, 0, err
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return 0, 0, errors.New("nothing at that offset")
	}

	c := buf[0]
	value := int64(c & 0x7f)
	used := int64(1)
	for c&0x80 != 0 {
		if int(used) >= len(buf) {
			return 0, 0, errors.New("the delta offset does not end")
		}
		c = buf[used]
		used++
		// Not a plain shift: each step adds one first, which is what
		// stops the same number having two encodings.
		value = ((value + 1) << 7) | int64(c&0x7f)
	}
	return value, used, nil
}

func (p *pack) hashAt(at int64) (Hash, error) {
	var h Hash
	if _, err := p.data.ReadAt(h[:], at); err != nil {
		return h, err
	}
	return h, nil
}

// inflateAt decompresses the stream starting at an offset. The compressed
// length is not written down anywhere, so the reader is given the rest of
// the file and stops itself when it has the bytes it was promised.
func (p *pack) inflateAt(at int64, size int) ([]byte, error) {
	section := io.NewSectionReader(p.data, at, p.size-at)
	unzip, err := zlib.NewReader(section)
	if err != nil {
		return nil, err
	}
	defer unzip.Close()

	out := make([]byte, size)
	if _, err := io.ReadFull(unzip, out); err != nil {
		return nil, err
	}
	return out, nil
}
