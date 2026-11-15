package media

import (
	"context"
	"testing"

	"github.com/pion/webrtc/v4"
)

// TestJoinAnswerAndLeave proves the signalling lifecycle the API depends on:
// a join yields a non-empty answer, the room counts participants, and leaving
// removes them. Real audio forwarding needs a browser or a full ICE loopback,
// which a unit test cannot stand up reliably; this covers the contract the API
// layer calls.
func TestJoinAnswerAndLeave(t *testing.T) {
	m := NewManager(func(_ int64, _ []byte) error { return nil })
	room := m.Room(7)
	// 客户端先出一份带音轨的 offer，服务端据此建 PeerConnection。
	pc, _ := newClient(t)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("local: %v", err)
	}
	// 第一次进场必须拿到非空 answer：客户端靠它完成协商。
	answer, err := room.Join(1, offer)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if answer.SDP == "" {
		t.Fatalf("empty answer")
	}
	// 人数是房间的唯一对外状态：进场必须让它加一。
	if n := room.Count(); n != 1 {
		t.Fatalf("count after join: got %d, want 1", n)
	}
	// 同一个人重复进场：旧连接被替换，人数不应该增长。
	pc2, _ := newClient(t)
	offer2, _ := pc2.CreateOffer(nil)
	_ = pc2.SetLocalDescription(offer2)
	if _, err := room.Join(1, offer2); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if n := room.Count(); n != 1 {
		t.Fatalf("count after rejoin: got %d, want 1", n)
	}
	// 离开必须把连接从房间摘掉，否则重连会累积幽灵参与者。
	room.Leave(1)
	if n := room.Count(); n != 0 {
		t.Fatalf("count after leave: got %d, want 0", n)
	}
	m.CloseAll(context.Background())
}

// newClient builds a loopback peer with one audio track attached.
func newClient(t *testing.T) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("peer: %v", err)
	}
	// 一条 Opus 音轨：测试只关心协商是否成立，不关心实际声音。
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "test")
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		t.Fatalf("add track: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc, track
}
