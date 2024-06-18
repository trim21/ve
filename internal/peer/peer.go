package peer

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/negrel/assert"

	"tyr/internal/pkg/empty"
	"tyr/internal/proto"
	"tyr/internal/req"
	"tyr/internal/util"
)

func New(conn io.ReadWriteCloser, infoHash metainfo.Hash, pieceNum uint32, addr string) *Peer {
	return newPeer(conn, infoHash, pieceNum, addr, true)
}

func NewIncoming(conn io.ReadWriteCloser, infoHash metainfo.Hash, pieceNum uint32, addr string) *Peer {
	return newPeer(conn, infoHash, pieceNum, addr, false)
}

func newPeer(conn io.ReadWriteCloser, infoHash metainfo.Hash, pieceNum uint32, addr string, skipHandshake bool) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Peer{
		ctx:       ctx,
		log:       log.With().Stringer("info_hash", infoHash).Str("addr", addr).Logger(),
		m:         sync.Mutex{},
		Conn:      conn,
		InfoHash:  infoHash,
		bitmapLen: util.BitmapLen(pieceNum),
		requests:  xsync.MapOf[req.Request, empty.Empty]{},
	}
	p.cancel = func() {
		p.dead.Store(true)
		cancel()
	}
	go p.start(skipHandshake)
	return p
}

var ErrPeerSendInvalidData = errors.New("peer send invalid data")

type Peer struct {
	log        zerolog.Logger
	ctx        context.Context
	Conn       io.ReadWriteCloser
	resChan    chan<- req.Response
	reqChan    chan req.Request
	cancel     context.CancelFunc
	requests   xsync.MapOf[req.Request, empty.Empty]
	Address    string
	Bitmap     bitmap.Bitmap
	m          sync.Mutex
	dead       atomic.Bool
	bitmapLen  uint32
	Choked     atomic.Bool
	Interested atomic.Bool
	InfoHash   torrent.InfoHash
}

type Event struct {
	Bitmap    bitmap.Bitmap
	Res       req.Response
	Req       req.Request
	Index     uint32
	Event     proto.Message
	keepAlive bool
}

func (p *Peer) DecodeEvents() (Event, error) {
	var b = make([]byte, 4)
	n, err := p.Conn.Read(b)
	if err != nil {
		return Event{}, err
	}

	assert.Equal(n, 4)

	l := binary.BigEndian.Uint32(b)

	// keep alive
	if l == 0 {
		// keep alive
		return Event{}, nil
	}

	p.log.Trace().Msgf("try to decode message with length %d", l)
	n, err = p.Conn.Read(b[:1])
	if err != nil {
		return Event{}, err
	}

	assert.Equal(n, 1)

	evt := proto.Message(b[0])
	p.log.Trace().Msgf("try to decode message event '%s'", evt)
	switch evt {
	case proto.Bitfield:
		return p.decodeBitfield(l)
	case proto.Have:
		return p.decodeHave(l)
	case proto.Interested, proto.NotInterested, proto.Choke, proto.Unchoke:
		return Event{Event: evt}, nil
	}

	// unknown events
	_, err = io.CopyN(io.Discard, p.Conn, int64(l-1))
	return Event{Event: evt}, err
}

func (p *Peer) start(skipHandshake bool) {
	defer p.cancel()
	if !skipHandshake {
		h, err := p.Handshake()
		if err != nil {
		}
		if h.InfoHash != p.InfoHash {
			p.log.Trace().Msgf("peer info hash mismatch %x", h.InfoHash)
			return
		}
		p.log.Trace().Msgf("connect to peer %s", url.QueryEscape(string(h.PeerID[:])))
	}

	go func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case r := <-p.reqChan:
				p.requests.Store(r, empty.Empty{})
				err := p.sendEvent(Event{
					Event: proto.Request,
					Req:   r,
				})
				// TODO: should handle error here
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		if p.ctx.Err() != nil {
			return
		}
		event, err := p.DecodeEvents()
		if err != nil {
			if errors.Is(err, ErrPeerSendInvalidData) {
				_ = p.Conn.Close()
				return
			}
			_ = p.Conn.Close()
			return
		}

		switch event.Event {
		case proto.Bitfield:
			p.Bitmap.Xor(event.Bitmap)
		case proto.Have:
			p.Bitmap.Set(event.Index)
		case proto.Interested:
			p.Interested.Store(true)
		case proto.NotInterested:
			p.Interested.Store(false)
		case proto.Choke:
			p.Choked.Store(true)
		case proto.Unchoke:
			p.Choked.Store(false)
		case proto.Piece:
			if !p.validateRes(event.Res) {
				// send response without requests
				_ = p.Conn.Close()
				return
			}
			p.resChan <- event.Res
		case proto.Request:
			p.reqChan <- event.Req
		}

		p.log.Trace().Msgf("receive %s event", event.Event)
	}
}

func (p *Peer) sendEvent(event Event) error {
	p.m.Lock()
	defer p.m.Unlock()

	if event.keepAlive {
		return proto.SendKeepAlive(p.Conn)
	}

	switch event.Event {
	case proto.Request:
		return proto.SendRequest(p.Conn, event.Req)
	}

	return nil
}

func (p *Peer) validateRes(res req.Response) bool {
	r := req.Request{
		PieceIndex: res.PieceIndex,
		Begin:      res.Begin,
		Length:     uint32(len(res.Data)),
	}

	if _, ok := p.requests.Load(r); ok {
		p.requests.Delete(r)
		return true
	}
	return false
}

func (p *Peer) Dead() bool {
	return p.dead.Load()
}
