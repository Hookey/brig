package server

import (
	"context"
	"fmt"
	"io/ioutil"
	"log/syslog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zombiezen.com/go/capnproto2/rpc"

	// For loadProfileServer
	_ "net/http/pprof"

	e "github.com/pkg/errors"
	"github.com/sahib/brig/backend"
	"github.com/sahib/brig/catfs"
	fserrs "github.com/sahib/brig/catfs/errors"
	"github.com/sahib/brig/events"
	"github.com/sahib/brig/fuse"
	"github.com/sahib/brig/gateway"
	p2pnet "github.com/sahib/brig/net"
	"github.com/sahib/brig/net/peer"
	"github.com/sahib/brig/repo"
	"github.com/sahib/brig/server/capnp"
	"github.com/sahib/brig/util/conductor"
	log "github.com/sirupsen/logrus"
)

type base struct {
	mu sync.Mutex

	// base path to the repository (i.e. BRIG_PATH)
	basePath string

	ctx context.Context

	repo       *repo.Repository
	mounts     *fuse.MountTable
	peerServer *p2pnet.Server

	// This the general backend, not a specific submodule one:
	backend backend.Backend
	quitCh  chan struct{}

	conductor *conductor.Conductor

	// gateway is the control object for the gateway server
	gateway *gateway.Gateway

	// evListener is a listener that will h
	evListener *events.Listener

	// evListenerCtx is the context for the event subsystem
	evListenerCtx context.Context

	// evListenerCancel can be called on quitting the daemon
	evListenerCancel context.CancelFunc

	// pprofPort is the port pprof can acquire profiling from
	pprofPort int
}

func repoIsInitialized(path string) error {
	data, err := ioutil.ReadFile(filepath.Join(path, "OWNER")) // #nosec
	if err != nil {
		return err
	}

	if len(data) == 0 {
		return fmt.Errorf("OWNER is empty")
	}

	return nil
}

// Handle is being called by the base server implementation
// for every local request that is being served to the brig daemon.
func (b *base) Handle(ctx context.Context, conn net.Conn) {
	transport := rpc.StreamTransport(conn)
	srv := capnp.API_ServerToClient(newAPIHandler(b))
	rpcConn := rpc.NewConn(
		transport,
		rpc.MainInterface(srv.Client),
		rpc.ConnLog(nil),
		rpc.SendBufferSize(128),
	)

	if err := rpcConn.Wait(); err != nil {
		log.Warnf("serving rpc failed: %v", err)
	}

	if err := rpcConn.Close(); err != nil {
		// Close seems to be complaining that the conn was
		// already closed, but be safe and expect this.
		if err != rpc.ErrConnClosed {
			log.Warnf("failed to close rpc conn: %v", err)
		}
	}
}

/////////

func (b *base) loadRepo() error {
	// Sanity check, so that we do not call a repo command without
	// an initialized repo. Error early for a meaningful message here.
	log.Infof("loading repository at %s", b.basePath)
	rp, err := repo.Open(b.basePath)
	if err != nil {
		log.Warningf("failed to load repository at `%s`: %v", b.basePath, err)
		return err
	}

	b.repo = rp

	// Adjust the backend's logging output here, since this should be done
	// before actually loading the backend (which might produce logs already)
	backendName := rp.Immutables.Backend()
	logName := fmt.Sprintf("brig-%s", backendName)
	wSyslog, err := syslog.New(syslog.LOG_NOTICE, logName)
	if err != nil {
		log.Warningf("Failed to open connection to syslog for ipfs: %v", err)
		log.Warningf("Will output ipfs logs to stderr for now")
		backend.ForwardLogByName(backendName, os.Stderr)
	} else {
		backend.ForwardLogByName(backendName, wSyslog)
	}

	return nil
}

func (b *base) loadProfileServer() {
	if !b.repo.Config.Bool("daemon.enable_pprof") {
		log.Debugf("not loading pprof; not enabled in config")
		return
	}

	log.Infof("loading pprof server")
	lst, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Warningf("failed to get a new port for the pprof server")
		return
	}

	port := lst.Addr().(*net.TCPAddr).Port
	log.Infof("Starting pprof server on :%d", port)

	go func() {
		defer lst.Close()

		if err := http.Serve(lst, nil); err != nil {
			log.Warningf("failed to serve pprof: %v", err)
		}
	}()

	b.pprofPort = port
}

/////////

func (b *base) loadBackend() error {
	backendName := b.repo.Immutables.Backend()
	log.Infof("loading backend `%s`", backendName)

	kr, err := b.repo.Keyring()
	if err != nil {
		return err
	}

	pubKey, err := kr.OwnPubKey()
	if err != nil {
		return err
	}

	fingerprint := peer.BuildFingerprint("", pubKey)

	realBackend, err := backend.FromName(
		backendName,
		b.repo.Config.String("daemon.ipfs_path"),
		fingerprint.PubKeyID(),
	)

	if err != nil {
		log.Errorf("Failed to load backend: %v", err)
		return err
	}

	b.backend = realBackend
	b.repo.StartAutoGCLoop(realBackend)
	return nil
}

/////////

func (b *base) loadPeerServer() error {
	log.Debugf("loading peer server")
	srv, err := p2pnet.NewServer(b.repo, b.backend, NewRemotesAPI(b))
	if err != nil {
		return err
	}

	go func() {
		if err := srv.Serve(); err != nil {
			log.Warningf("PeerServer.Serve() returned with error: %v", err)
		}
	}()

	b.peerServer = srv

	// Initially sync the ping map:
	addrs := []string{}
	remotes, err := b.repo.Remotes.ListRemotes()
	if err != nil {
		return err
	}

	for _, remote := range remotes {
		addrs = append(addrs, remote.Fingerprint.Addr())
	}

	if err := srv.PingMap().Sync(addrs); err != nil {
		return err
	}

	self, err := b.backend.Identity()
	if err != nil {
		return err
	}

	b.evListenerCtx, b.evListenerCancel = context.WithCancel(context.Background())
	b.evListener = events.NewListener(
		b.repo.Config.Section("events"),
		b.backend,
		self.Addr,
	)

	b.evListener.RegisterEventHandler(events.FsEvent, false, b.handleFsEvent)
	if err := b.evListener.SetupListeners(b.evListenerCtx, addrs); err != nil {
		log.Warningf("failed to setup event listeners: %v", err)
	}

	// Give peer server a small bit of time to start up, so it can Accept()
	// connections immediately after loadPeerServer. Also nice for tests.
	time.Sleep(50 * time.Millisecond)

	if err := b.initialSyncWithAutoUpdatePeers(); err != nil {
		log.Warningf("initial sync failed with one or more peers: %v", err)
	}

	// Now that we boooted up, we should tell other users that our fs changed.
	// It may or may not have, but other remotes judge that.
	b.notifyFsChangeEvent()
	return nil
}

//////

func (b *base) loadGateway() error {
	log.Debugf("loading gateway")

	rapi := NewRemotesAPI(b)
	return b.withCurrFs(func(fs *catfs.FS) error {
		gateway, err := gateway.NewGateway(
			fs,
			rapi,
			b.repo.Config.Section("gateway"),
			b.evListener,
			filepath.Join(b.repo.BaseFolder, "gateway"),
		)

		if err != nil {
			return err
		}

		b.gateway = gateway
		b.gateway.Start()
		return nil
	})
}

/////////

type mountNotifier struct {
	b *base
}

func (mn mountNotifier) PublishEvent() {
	mn.b.notifyFsChangeEvent()
}

func (b *base) loadMounts() error {
	return b.withCurrFs(func(fs *catfs.FS) error {
		b.mounts = fuse.NewMountTable(fs, mountNotifier{b: b})
		return nil
	})
}

/////////

func (b *base) loadAll() error {
	if err := b.loadRepo(); err != nil {
		return err
	}

	if err := b.loadBackend(); err != nil {
		return err
	}

	if err := b.loadMounts(); err != nil {
		return err
	}

	if err := b.loadPeerServer(); err != nil {
		return err
	}

	if err := b.loadGateway(); err != nil {
		return err
	}

	b.loadProfileServer()
	return nil
}

/////////

func (b *base) withCurrFs(fn func(fs *catfs.FS) error) error {
	user := b.repo.CurrentUser()
	fs, err := b.repo.FS(user, b.backend)
	if err != nil {
		return err
	}

	return fn(fs)
}

func (b *base) withRemoteFs(owner string, fn func(fs *catfs.FS) error) error {
	fs, err := b.repo.FS(owner, b.backend)
	if err != nil {
		return err
	}

	return fn(fs)
}

func (b *base) withFsFromPath(path string, fn func(url *URL, fs *catfs.FS) error) error {
	url, err := parsePath(path)
	if err != nil {
		return err
	}

	if url.User == "" {
		return b.withCurrFs(func(fs *catfs.FS) error {
			return fn(url, fs)
		})
	}

	return b.withRemoteFs(url.User, func(fs *catfs.FS) error {
		return fn(url, fs)
	})
}

func (b *base) withNetClient(who string, fn func(ctl *p2pnet.Client) error) error {
	subCtx, cancel := context.WithCancel(b.ctx)
	defer cancel()

	ctl, err := p2pnet.Dial(subCtx, who, b.repo, b.backend, b.peerServer.PingMap())
	if err != nil {
		return e.Wrapf(err, "dial")
	}

	if err := fn(ctl); err != nil {
		ctl.Close()
		return err
	}

	return ctl.Close()
}

func (b *base) Quit() (err error) {
	log.Info("shutting down brigd due to QUIT command")

	if err := b.gateway.Stop(); err != nil {
		log.Warningf("could not close gateway: %v", err)
	}

	if err := b.gateway.Close(); err != nil {
		log.Warningf("could not shut down gateway: %v", err)
	}

	log.Infof("closing peer server...")
	if err = b.peerServer.Close(); err != nil {
		log.Warningf("failed to close peer server: %v", err)
	}

	b.evListenerCancel()
	log.Infof("shutting down event listener...")
	if b.evListener != nil {
		if err := b.evListener.Close(); err != nil {
			log.Warningf("shutting down event handler failed: %v", err)
		}
	}

	log.Infof("trying to lock repository...")

	if err = b.repo.Close(); err != nil {
		log.Warningf("failed to lock repository: %v", err)
	}

	log.Infof("trying to unmount any mounts...")
	if err := b.mounts.Close(); err != nil {
		return err
	}

	log.Infof("===== brigd can be considered dead now! ====")
	return nil
}

func newBase(
	ctx context.Context,
	basePath string,
	quitCh chan struct{},
) *base {
	return &base{
		ctx:       ctx,
		basePath:  basePath,
		quitCh:    quitCh,
		conductor: conductor.New(5*time.Minute, 100),
	}
}

func (b *base) doFetch(who string) error {
	owner := b.repo.Immutables.Owner()
	if who == owner {
		log.Infof("skipping fetch for own metadata")
		return nil
	}

	return b.withNetClient(who, func(ctl *p2pnet.Client) error {
		return b.withRemoteFs(who, func(remoteFs *catfs.FS) error {
			// Not all remotes might allow doing a full fetch.
			// This is only possible when having full access to all folders.
			if isAllowed, err := ctl.IsCompleteFetchAllowed(); isAllowed && err != nil {
				log.Debugf("fetch: doing complete fetch for %s", who)
				storeBuf, err := ctl.FetchStore()
				if err != nil {
					return e.Wrapf(err, "fetch-store")
				}

				return e.Wrapf(remoteFs.Import(storeBuf), "import")
			}

			// Ask our local copy of the remote what the last patch index was.
			fromIndex, err := remoteFs.LastPatchIndex()
			if err != nil {
				return err
			}

			// Get the missing changes since then:
			log.Infof("fetch: doing partial fetch for %s starting at %d", who, fromIndex)
			patches, err := ctl.FetchPatches(fromIndex)
			if err != nil {
				return err
			}

			return remoteFs.ApplyPatches(patches)
		})
	})
}

func (b *base) doSync(withWhom string, needFetch bool, msg string) (*catfs.Diff, error) {
	if needFetch {
		if err := b.doFetch(withWhom); err != nil {
			return nil, e.Wrapf(err, "fetch")
		}
	}

	var diff *catfs.Diff

	return diff, b.withCurrFs(func(ownFs *catfs.FS) error {
		return b.withRemoteFs(withWhom, func(remoteFs *catfs.FS) error {
			// Automatically make a commit before merging with their state:
			timeStamp := time.Now().UTC().Format(time.RFC3339)
			commitMsg := fmt.Sprintf("sync with %s on %s", withWhom, timeStamp)
			if err := ownFs.MakeCommit(commitMsg); err != nil && err != fserrs.ErrNoChange {
				return e.Wrapf(err, "merge-commit")
			}

			cmtBefore, err := ownFs.Head()
			if err != nil {
				return err
			}

			log.Debugf("Starting sync with %s", withWhom)

			rmt, err := b.repo.Remotes.Remote(withWhom)
			if err != nil {
				return err
			}

			err = ownFs.Sync(
				remoteFs,
				catfs.SyncOptMessage(msg),
				catfs.SyncOptConflictStrategy(rmt.ConflictStrategy),
				catfs.SyncOptReadOnlyFolders(rmt.ReadOnlyFolders()),
				catfs.SyncOptConflictgStrategyPerFolder(rmt.ConflictStrategyPerFolder()),
			)

			if err != nil {
				return err
			}

			log.Debugf("Sync with %s done", withWhom)

			cmtAfter, err := ownFs.Head()
			if err != nil {
				return err
			}

			diff, err = ownFs.MakeDiff(ownFs, cmtBefore, cmtAfter)
			return err
		})
	})
}

func (b *base) handleFsEvent(ev *events.Event) {
	rmt, err := b.repo.Remotes.RemoteByAddr(ev.Source)
	if err != nil {
		log.Debugf("failed to resolve '%s' to a known remote name: %v", ev.Source, err)
		return
	}

	if !rmt.AcceptAutoUpdates {
		return
	}

	log.Infof("doing sync with »%s« since we received an update notification.", rmt.Name)

	msg := fmt.Sprintf("sync due to notification from »%s«", rmt.Name)
	if _, err := b.doSync(rmt.Name, true, msg); err != nil {
		log.Warningf("sync failed: %v", err)
	}
}

func (b *base) notifyFsChangeEvent() {
	if b.evListener == nil {
		return
	}

	// Do not trigger events when we're looking at the store of somebody else.
	owner := b.repo.Immutables.Owner()
	if owner != b.repo.CurrentUser() {
		return
	}

	ev := events.Event{
		Type: events.FsEvent,
	}

	if err := b.evListener.PublishEvent(ev); err != nil {
		log.Warningf("failed to publish filesystem change event: %v", err)
	}
}

func (b *base) initialSyncWithAutoUpdatePeers() error {
	rmts, err := b.repo.Remotes.ListRemotes()
	if err != nil {
		return err
	}

	for _, rmt := range rmts {
		if !rmt.AcceptAutoUpdates {
			continue
		}

		msg := fmt.Sprintf("sync with »%s« due to initial auto-update", rmt.Name)
		if _, err := b.doSync(rmt.Name, true, msg); err != nil {
			log.Warningf("failed to sync initially with %s: %v", rmt.Name, err)
		}
	}

	return nil
}

func (b *base) syncRemoteStates() error {
	addrs := []string{}
	remotes, err := b.repo.Remotes.ListRemotes()
	if err != nil {
		return err
	}

	for _, remote := range remotes {
		addrs = append(addrs, remote.Fingerprint.Addr())
	}

	pmap := b.peerServer.PingMap()
	if err := pmap.Sync(addrs); err != nil {
		return err
	}

	return b.evListener.SetupListeners(b.evListenerCtx, addrs)
}
