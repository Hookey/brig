// +build !windows

package fuse

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"sync"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	e "github.com/pkg/errors"
	"github.com/sahib/brig/catfs"
	"github.com/sahib/brig/util"
	log "github.com/sirupsen/logrus"
)

// Notifier implementors can take notifications
// from any events happening in the fuse mount.
type Notifier interface {
	// PublishEvent is called whenever a modification happens.
	PublishEvent()
}

// MountOptions defines all possible knobs you can turn for a mount.
// The zero value are the default options.
type MountOptions struct {
	// ReadOnly makes the mount not modifyable
	ReadOnly bool
	// Root determines what the root directory is.
	Root string
	// Offline tells the mount to error out on files that would need
	// to be fetched from far.
	Offline bool
}

// This is very similar (and indeed mostly copied) code from:
// https://github.com/bazil/fuse/blob/master/fs/fstestutil/mounted.go
// Since that's "only" test module, api might change, so better have this
// code here (also we might do a few things differently).

// Mount represents a fuse endpoint on the filesystem.
// It is used as top-level API to control a brigfs fuse mount.
type Mount struct {
	Dir string

	filesys  *Filesystem
	closed   bool
	done     chan util.Empty
	errors   chan error
	conn     *fuse.Conn
	server   *fs.Server
	options  MountOptions
	notifier Notifier
	fs       *catfs.FS
}

// NewMount mounts a fuse endpoint at `mountpoint` retrieving data from `store`.
func NewMount(cfs *catfs.FS, mountpoint string, notifier Notifier, opts MountOptions) (*Mount, error) {
	mountOptions := []fuse.MountOption{
		fuse.FSName("brigfs"),
		fuse.Subtype("brig"),
		fuse.AllowNonEmptyMount(),
		// enabling MaxReadahead double or even triple Read throughput 12MB/s -> 25 or 33 MB/s
		fuse.MaxReadahead(128 * 1024), // kernel uses at max 128kB = 131072B
		// enabling WritebackCache doubles write speed to buffer 12MB/s -> 24MB/s
		fuse.WritebackCache(), // writes will happen in mach large blocks 128kB instead of 8kB
	}

	if opts.ReadOnly {
		mountOptions = append(mountOptions, fuse.ReadOnly())
	}

	log.Debugf("PATH: %v", os.Getenv("PATH"))
	conn, err := fuse.Mount(mountpoint, mountOptions...)
	if err != nil {
		return nil, e.Wrapf(err, "fuse-mount")
	}

	if opts.Root == "" {
		opts.Root = "/"
	}

	info, err := cfs.Stat(opts.Root)
	if err != nil {
		return nil, e.Wrapf(err, "failed to lookup root node of mount: %v", mountpoint)
	}

	if !info.IsDir {
		return nil, e.Wrapf(err, "%s is not a directory", opts.Root)
	}

	mnt := &Mount{
		conn:     conn,
		server:   fs.New(conn, nil),
		Dir:      mountpoint,
		done:     make(chan util.Empty),
		errors:   make(chan error),
		options:  opts,
		notifier: notifier,
		fs:       cfs,
	}
	filesys := &Filesystem{m: mnt, root: opts.Root}
	mnt.filesys = filesys

	go func() {
		defer close(mnt.done)
		log.Debugf("serving fuse mount at %v", mountpoint)
		mnt.errors <- mnt.server.Serve(filesys)
		mnt.done <- util.Empty{}
		log.Debugf("stopped serving fuse at %v", mountpoint)
	}()

	select {
	case <-mnt.conn.Ready:
		if err := mnt.conn.MountError; err != nil {
			return nil, err
		}
	case err = <-mnt.errors:
		// Serve quit early
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Serve exited early")
	}

	return mnt, nil
}

func lazyUnmount(dir string) error {
	cmd := exec.Command("fusermount", "-u", "-z", dir) // #nosec
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			output = bytes.TrimRight(output, "\n")
			msg := err.Error() + ": " + string(output)
			err = errors.New(msg)
		}
		return err
	}
	return nil
}

// EqualOptions returns true when the options in `opts` have the same
// option as currently set in the mount. If so, no re-mount is required.
func (m *Mount) EqualOptions(opts MountOptions) bool {
	if m.options.ReadOnly != opts.ReadOnly {
		return false
	}

	return path.Clean(m.options.Root) == path.Clean(opts.Root)
}

// Close will wait until all I/O operations are done and unmount the fuse
// mount again.
func (m *Mount) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true

	log.Infof("unmounting fuse mount at %v (this might take a bit)", m.Dir)

	couldUnmount := false
	waitTimeout := 1 * time.Second

	// Attempt unmounting several times:
	for tries := 0; tries < 10; tries++ {
		if err := fuse.Unmount(m.Dir); err != nil {
			log.Debugf("failed to graceful unmount: %v", err)
			time.Sleep(250 * time.Millisecond)
			continue
		}

		couldUnmount = true
		waitTimeout = 5 * time.Second
		break
	}

	if !couldUnmount {
		log.Warn("cant properly unmount; are there still processes using the mount?")
		log.Warn("attempting lazy umount (you might leak resources!)")
		if err := lazyUnmount(m.Dir); err != nil {
			log.Debugf("lazy unmount failed: %v", err)
		}
	}

	// Be sure to drain the error channel:
	select {
	case err := <-m.errors:
		if err != nil {
			log.Warningf("fuse returned an error: %v", err)
		}
	case <-time.NewTimer(waitTimeout).C:
		// blocking due to fuse freeze.
	}

	// Be sure to pull the item from the channel:
	select {
	case <-m.done:
		log.Debugf("gracefully shutting down")
	case <-time.NewTimer(waitTimeout).C:
		// success or blocking due to fuse freeze.
	}

	// If we could not unmount, schedule closing in the background.
	// This might be leaky, since Close might not ever return.
	// But usually we unmount on program exit anyways...
	if couldUnmount {
		if err := m.conn.Close(); err != nil {
			return err
		}
	} else {
		go m.conn.Close()
	}

	return nil
}

// MountTable is a mapping from the mountpoint to the respective
// `Mount` struct. It's given as convenient way to maintain several mounts.
// All operations on the table are safe to call from several goroutines.
type MountTable struct {
	mu       sync.Mutex
	m        map[string]*Mount
	fs       *catfs.FS
	notifier Notifier
}

// NewMountTable returns an empty mount table.
func NewMountTable(fs *catfs.FS, notifier Notifier) *MountTable {
	return &MountTable{
		m:        make(map[string]*Mount),
		fs:       fs,
		notifier: notifier,
	}
}

// AddMount calls NewMount and adds it to the table at `path`.
func (t *MountTable) AddMount(path string, opts MountOptions) (*Mount, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.addMount(path, opts)
}

func checkMountPath(path string) error {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		// This will also fail if `path` is not a directory:
		return err
	}

	if len(files) > 0 {
		return fmt.Errorf("Refusing to mount over non-empty dir `%s`", path)
	}

	return nil
}

func (t *MountTable) addMount(path string, opts MountOptions) (*Mount, error) {
	if err := checkMountPath(path); err != nil {
		return nil, e.Wrapf(err, "dir check")
	}

	m, ok := t.m[path]
	if ok {
		return m, nil
	}

	m, err := NewMount(t.fs, path, t.notifier, opts)
	if err == nil {
		t.m[path] = m
	}

	return m, e.Wrapf(err, "new-mount")
}

// Unmount closes the mount at `path` and deletes it from the table.
func (t *MountTable) Unmount(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.unmount(path)
}

func (t *MountTable) unmount(path string) error {
	m, ok := t.m[path]
	if !ok {
		return fmt.Errorf("no mount at `%v`", path)
	}

	delete(t.m, path)
	return m.Close()
}

// Close unmounts all leftover mounts and clears the table.
func (t *MountTable) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var err error

	for _, mount := range t.m {
		if closeErr := mount.Close(); closeErr != nil {
			err = closeErr
		}
	}

	t.m = make(map[string]*Mount)
	return err
}
