package media

// 房间音频的 SFU：每个参与者一条 PeerConnection，发布上来的音频转发给房间里
// 其他所有人。它不是「把包原样广播」——每个订阅者一条独立连接，所以一万人
// 听同一路音频，服务端就要发一万份。

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/pion/webrtc/v4"
)

// Signal is one SDP exchange pushed to a client over the websocket. Type is
// "offer" or "answer"; the client answers an offer and applies an answer.
type Signal struct {
	Type  string `json:"type"`
	SDP   string `json:"sdp"`
	Room  int64  `json:"room_id"`
	From  int64  `json:"from"`
}

// Pusher delivers one signal frame to one user. The media package does not know
// about websockets or the bus; the API layer supplies this.
type Pusher func(userID int64, payload []byte) error

// Participant is one connected client inside a room.
type Participant struct {
	userID int64
	pc     *webrtc.PeerConnection
	// published holds the local tracks this participant is sending. They are
	// re-offered to every peer that joins later.
	published []*webrtc.TrackLocalStaticRTP
}

// Room owns the peer connections of one room. It is created on first join and
// dropped when the last participant leaves.
type Room struct {
	id    int64
	push  Pusher
	mu    sync.Mutex
	peers map[int64]*Participant
}

// Manager creates rooms on demand and keeps them until they empty.
type Manager struct {
	push  Pusher
	mu    sync.Mutex
	rooms map[int64]*Room
}

// NewManager returns a manager that pushes signalling through push.
func NewManager(push Pusher) *Manager {
	return &Manager{push: push, rooms: map[int64]*Room{}}
}

// Room returns the room with this id, creating it on first use.
func (m *Manager) Room(id int64) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		r = &Room{id: id, push: m.push, peers: map[int64]*Participant{}}
		m.rooms[id] = r
	}
	return r
}

// newPeerConnection builds one peer with a single audio transceiver. STUN is
// left to the deployment: a single machine demo does not need it, a public one
// must point at its own TURN.
func newPeerConnection() (*webrtc.PeerConnection, error) {
	cfg := webrtc.Configuration{}
	return webrtc.NewPeerConnection(cfg)
}

// Join creates the participant's peer connection and applies their offer.
// The answer is returned to the caller; tracks already published in the room
// are added before the answer is built, so one round trip is enough.
func (r *Room) Join(userID int64, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	pc, err := newPeerConnection()
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	p := &Participant{userID: userID, pc: pc}
	r.mu.Lock()
	// 同一账号重复进场：先清掉旧连接，否则会有两条连接抢同一个用户。
	if old, ok := r.peers[userID]; ok {
		_ = old.pc.Close()
	}
	r.peers[userID] = p
	others := make([]*Participant, 0, len(r.peers))
	for id, o := range r.peers {
		if id != userID {
			others = append(others, o)
		}
	}
	r.mu.Unlock()

	// 把房间里已有的音轨加到新连接上：新来的人应该立刻听到正在说话的人。
	for _, o := range others {
		for _, t := range o.published {
			if _, err := pc.AddTrack(t); err != nil {
				return webrtc.SessionDescription{}, err
			}
		}
	}
	// 新来的人自己发布的音轨，收到后转发给房间里的其他人。
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		r.relay(p, tr)
	})
	if err := pc.SetRemoteDescription(offer); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return pc.CreateAnswer(nil)
}

// relay turns one published track into a local track and offers it to every
// other participant.
func (r *Room) relay(from *Participant, tr *webrtc.TrackRemote) {
	local, err := webrtc.NewTrackLocalStaticRTP(tr.Codec().RTPCodecCapability, tr.ID(), tr.StreamID())
	if err != nil {
		return
	}
	// 记下来源轨：后来进场的人要把这些轨一并加上，否则只能听到开播之后的声音。
	from.published = append(from.published, local)
	r.mu.Lock()
	others := make([]*Participant, 0, len(r.peers))
	for id, p := range r.peers {
		if id != from.userID {
			others = append(others, p)
		}
	}
	r.mu.Unlock()
	// 给每个已在场的人加这条轨，然后各自重新协商。
	for _, p := range others {
		if _, err := p.pc.AddTrack(local); err != nil {
			continue
		}
		// 加轨之后必须重新协商，否则对端不会开始收包。
		r.renegotiate(p)
	}
	// 读循环把 RTP 从远端搬到本地轨道；不读就会一直缓冲。
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := tr.Read(buf)
			if err != nil {
				return
			}
			if _, err := local.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
}

// renegotiate offers the current local description to one participant.
func (r *Room) renegotiate(p *Participant) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return
	}
	// 用总线把 offer 推给这个客户端；它答完之后走 /rtc/answer。
	// 外层包一个 rtc 类型，客户端才能把它和普通消息帧分开。
	inner, err := json.Marshal(Signal{Type: "offer", SDP: offer.SDP, Room: r.id})
	if err != nil {
		return
	}
	payload, err := json.Marshal(map[string]any{"type": "rtc", "signal": json.RawMessage(inner)})
	if err != nil {
		return
	}
	_ = r.push(p.userID, payload)
}

// AcceptAnswer applies the answer a client sent back after a renegotiation.
func (r *Room) AcceptAnswer(userID int64, answer webrtc.SessionDescription) error {
	r.mu.Lock()
	p, ok := r.peers[userID]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return p.pc.SetRemoteDescription(answer)
}

// Leave closes the participant's connection and forgets them.
func (r *Room) Leave(userID int64) {
	r.mu.Lock()
	p, ok := r.peers[userID]
	if ok {
		delete(r.peers, userID)
	}
	r.mu.Unlock()
	if ok {
		_ = p.pc.Close()
	}
}

// Count returns how many participants are currently connected.
func (r *Room) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.peers)
}

// CloseAll closes every participant. It is called on shutdown.
func (m *Manager) CloseAll(_ context.Context) {
	m.mu.Lock()
	rooms := m.rooms
	m.rooms = map[int64]*Room{}
	m.mu.Unlock()
	// 逐个房间关连接：进程退出前把 PeerConnection 收干净。
	for _, r := range rooms {
		r.mu.Lock()
		for _, p := range r.peers {
			_ = p.pc.Close()
		}
		r.peers = map[int64]*Participant{}
		r.mu.Unlock()
	}
}
