package store

// User is an account row. IsAdmin is the platform-admin flag; it says nothing
// about what the user may do inside any conversation.
type User struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	Status      string
	CreatedAt   int64
}

// Conversation is a chat row. NextSeq is the sequence high-water mark the write
// path increments; nothing else is allowed to touch it. IsRoom marks a room:
// same storage and sequence, different delivery promise.
type Conversation struct {
	ID          int64
	CommunityID int64
	IsRoom      bool
	Kind        string
	Title       string
	OwnerID     int64
	NextSeq     int64
	CreatedAt   int64
}

// Message is one immutable row of history. RecallOf points at the sequence this
// row voids, and is zero for everything except tombstones. Delivery records the
// promise this row was written under: at_least_once or broadcast.
type Message struct {
	ConversationID int64
	Seq            int64
	ID             string
	ClientMsgID    string
	SenderID       int64
	Kind           string
	Body           string
	Meta           string
	RecallOf       int64
	Delivery       string
	CreatedAt      int64
}
