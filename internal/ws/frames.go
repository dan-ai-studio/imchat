package ws

// ── 阅读导览 · 第 3 站 ────────────────────────────────────
// 上一站：站2 一条连接
// 下一站：站4 发送的前半

import "encoding/json"

// Frame is the one shape that travels over the socket, in both directions. Not
// every field belongs to every frame type; the omitempty tags keep the unused
// ones off the wire. The table below says which type carries which fields.
type Frame struct {
	Type           string `json:"type"`
	Device         string `json:"device,omitempty"`
	ClientMsgID    string `json:"client_msg_id,omitempty"`
	ConversationID int64  `json:"conversation_id,omitempty"`
	Seq            int64  `json:"seq,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Body           string `json:"body,omitempty"`
	ID             string `json:"id,omitempty"`
	SenderID       int64  `json:"sender_id,omitempty"`
	CreatedAt      int64  `json:"created_at,omitempty"`
	UserID         int64  `json:"user_id,omitempty"`
	Count          int    `json:"count,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
}

// ── 帧清单（照代码核实，不是照文档抄）────────────────────
//
// 上行：客户端 → 服务端
//
//	ping   保活，定时发，回一帧 pong
//	send   发一条消息，用户按下发送时发；必带 client_msg_id，它是重发的幂等键
//	ack    确认收到某条消息，消息真的进了界面才发；只带会话与序号，不带内容
//	read   推进已读位点，用户把会话滚到底时发；会向同会话的其他终端广播
//
// 下行：服务端 → 客户端
//
//	ready  连上之后的第一帧，告诉客户端服务端认到的是谁
//	msg    一条新消息，或者一条撤回墓碑
//	sent   回执：服务端已经收下你的 send。注意它不等于投递成功，它只说明写进库了
//	pong   保活回应
//	read   有人在某个会话里推进了已读位点
//	error  出错的统一出口，带 code 与 message
//
// 只有一帧会改状态：ack。它是唯一能让一条待投递记录被消解的帧，
// 其余帧都只是在搬运消息——这就是「为什么是它」的答案。
//
// 协议里没有「撤回帧」：撤回是一条 kind=recall 的普通消息，见 ADR-0005。
//
// 一处与文档不符，以代码为准：README 与 ADR-0004 都提到一个 hello 帧，
// 说它携带 last_seq 用来回补缺口。代码里没有这个帧——缺口补齐是客户端
// 连上之后自己调历史接口完成的（README 的「已知限制」里也这么写）。

// Encode marshals a frame. A marshal failure yields an empty object rather than
// a hand-written fallback string, which keeps the wire contract JSON-only.
func Encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
