package task

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// defaultSegFileCacheSize bounds the read handles the index keeps open. A store
// that has rotated for years has hundreds of segments with five log files each;
// macOS defaults to 256 open files per process, Windows has its own limits.
const defaultSegFileCacheSize = 64

type segFileKey struct {
	ord  uint16
	kind fileKind
}

type cachedSegFile struct {
	file *os.File
	refs int
	used uint64
}

// segFileCache hands out read handles to the log files of every segment. A
// handle is reference counted: the least recently used handle that nobody holds
// is closed when the cache is over its limit, so a read in flight never has its
// file closed under it.
type segFileCache struct {
	mu     sync.Mutex
	dirs   []string // dirs[ordinal]
	files  map[segFileKey]*cachedSegFile
	max    int
	clock  uint64
	closed bool
}

func newSegFileCache() *segFileCache {
	return &segFileCache{files: make(map[segFileKey]*cachedSegFile), max: defaultSegFileCacheSize}
}

// setDir records where the files of segment ord live. A segment's directory
// never changes once it is known.
func (c *segFileCache) setDir(ord uint16, dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.dirs) <= int(ord) {
		c.dirs = append(c.dirs, "")
	}
	c.dirs[ord] = dir
}

// acquire opens (or reuses) the file of kind in segment ord for reading and
// returns it with the function that gives it back. A file that does not exist
// is reported as an error wrapping os.ErrNotExist.
func (c *segFileCache) acquire(ord uint16, kind fileKind) (*os.File, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, os.ErrClosed
	}
	key := segFileKey{ord: ord, kind: kind}
	c.clock++
	entry, ok := c.files[key]
	if !ok {
		if int(ord) >= len(c.dirs) || c.dirs[ord] == "" {
			return nil, nil, fmt.Errorf("task index has no directory for segment %d", ord)
		}
		file, err := os.Open(filepath.Join(c.dirs[ord], kindFilename[kind]))
		if err != nil {
			return nil, nil, err
		}
		entry = &cachedSegFile{file: file}
		c.files[key] = entry
		c.evictLocked(key)
	}
	entry.refs++
	entry.used = c.clock
	return entry.file, func() { c.release(key, entry) }, nil
}

func (c *segFileCache) release(key segFileKey, entry *cachedSegFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.refs--
	if c.closed && entry.refs == 0 {
		_ = entry.file.Close()
		delete(c.files, key)
	}
}

// evictLocked closes unreferenced handles, oldest first, until the cache is
// within its limit. keep is never evicted.
func (c *segFileCache) evictLocked(keep segFileKey) {
	for len(c.files) > c.max {
		var victim segFileKey
		var oldest uint64
		found := false
		for key, entry := range c.files {
			if key == keep || entry.refs > 0 {
				continue
			}
			if !found || entry.used < oldest {
				victim, oldest, found = key, entry.used, true
			}
		}
		if !found {
			return
		}
		_ = c.files[victim].file.Close()
		delete(c.files, victim)
	}
}

// openCount is the number of handles currently open (tests).
func (c *segFileCache) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.files)
}

// close closes every handle nobody holds; a handle in use is closed when it is
// released. After close, acquire fails with os.ErrClosed.
func (c *segFileCache) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	var first error
	for key, entry := range c.files {
		if entry.refs > 0 {
			continue
		}
		if err := entry.file.Close(); err != nil && first == nil {
			first = err
		}
		delete(c.files, key)
	}
	return first
}
