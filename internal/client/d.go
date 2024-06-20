package client

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	stdSync "sync/atomic"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types/infohash"
	"github.com/mxk/go-flowrate/flowrate"
	"github.com/negrel/assert"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/atomic"

	"tyr/internal/pkg/bm"
	"tyr/internal/req"
)

type State uint8

//go:generate stringer -type=State
const Downloading State = 0
const Stopped State = 1
const Uploading State = 2
const Checking State = 3
const Moving State = 3
const Error State = 4

// Download manage a download task
// ctx should be canceled when torrent is removed, not stopped.
type Download struct {
	info              metainfo.Info
	meta              metainfo.MetaInfo
	reqHistory        *xsync.MapOf[uint32, downloadReq]
	log               zerolog.Logger
	ctx               context.Context
	err               error
	cancel            context.CancelFunc
	cond              *sync.Cond
	c                 *Client
	ioDown            *flowrate.Monitor
	ioUp              *flowrate.Monitor
	ResChan           chan req.Response
	conn              *xsync.MapOf[netip.AddrPort, *Peer]
	connectionHistory *xsync.MapOf[netip.AddrPort, connHistory]
	bm                *bm.Bitmap
	PieceData         *xsync.MapOf[uint32, []byte]
	basePath          string
	key               string
	downloadDir       string
	tags              []string
	pieceInfo         []pieceInfo
	trackers          []TrackerTier
	peers             []netip.AddrPort
	totalLength       int64
	downloaded        atomic.Int64
	done              atomic.Bool
	uploaded          atomic.Int64
	completed         atomic.Int64
	checkProgress     atomic.Int64
	uploadAtStart     int64
	downloadAtStart   int64
	lazyInitialized   atomic.Bool
	seq               atomic.Bool
	m                 sync.RWMutex
	peersMutex        sync.RWMutex
	connMutex         sync.RWMutex
	numPieces         uint32
	announcePending   stdSync.Bool
	hash              metainfo.Hash
	peerID            PeerID
	state             State
	private           bool
}

func (c *Client) NewDownload(m *metainfo.MetaInfo, info metainfo.Info, basePath string, tags []string) *Download {
	var private bool
	if info.Private != nil {
		private = *info.Private
	}

	ctx, cancel := context.WithCancel(context.Background())

	var n = info.NumPieces()

	var infoHash = m.HashInfoBytes()
	d := &Download{
		ctx:      ctx,
		cancel:   cancel,
		meta:     *m,
		c:        c,
		log:      log.With().Hex("info_hash", infoHash.Bytes()).Logger(),
		state:    Checking,
		peerID:   NewPeerID(),
		tags:     tags,
		basePath: basePath,

		reqHistory: xsync.NewMapOf[uint32, downloadReq](),

		ioDown: flowrate.New(time.Second, time.Second),
		ioUp:   flowrate.New(time.Second, time.Second),

		totalLength:       info.TotalLength(),
		info:              info,
		hash:              infoHash,
		conn:              xsync.NewMapOf[netip.AddrPort, *Peer](),
		connectionHistory: xsync.NewMapOf[netip.AddrPort, connHistory](),

		pieceInfo: buildPieceInfos(info),
		numPieces: uint32(n),
		PieceData: xsync.NewMapOf[uint32, []byte](),

		//key:
		// there maybe 1 uint64 extra data here.
		bm:          bm.New(),
		private:     private,
		downloadDir: basePath,
	}

	d.seq.Store(true)
	d.cond = sync.NewCond(&d.m)

	assert.Equal(uint32(len(d.pieceInfo)), d.numPieces)

	d.setAnnounceList(m)

	return d
}

func (d *Download) Move(target string) error {
	return errors.New("not implemented")
}

func (d *Download) Display() string {
	d.m.RLock()
	defer d.m.RUnlock()
	buf := bytebufferpool.Get()
	defer bytebufferpool.Put(buf)
	_, _ = fmt.Fprintf(buf, "%s | %.20s | %.2f%% | %d ↓", d.state, d.info.Name, float64(d.completed.Load())/float64(d.totalLength)*100.0, d.conn.Size())
	for _, tier := range d.trackers {
		for _, t := range tier.trackers {
			t.RLock()
			fmt.Fprintf(buf, " ( %d %s )", t.peerCount, t.url)
			if t.err != nil {
				_, _ = fmt.Fprintf(buf, " | %s", t.err)
			}
			t.RUnlock()
		}
	}
	return buf.String()
}

// if download encounter an error must stop downloading/uploading
func (d *Download) setError(err error) {
	d.m.Lock()
	d.err = err
	d.state = Error
	d.m.Unlock()
}

func canonicalName(info metainfo.Info, infoHash infohash.T) string {
	// yes, there are some torrent have this name
	name := info.Name
	if (info.NameUtf8) != "" {
		name = info.NameUtf8
	}

	if name == "" {
		return infoHash.HexString()
	}

	if len(info.Files) != 0 {
		return name
	}
	s := strings.Split(name, ".")
	if len(s) == 0 {
		return name
	}

	return strings.Join(s[:len(s)-1], ".")
}

type connHistory struct {
	lastTry   time.Time
	err       error
	timeout   bool
	connected bool
}
