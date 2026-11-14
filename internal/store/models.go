package store

type User struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	Status      string
	CreatedAt   int64
}

type Conversation struct {
	ID        int64
	Kind      string
	Title     string
	OwnerID   int64
	NextSeq   int64
	CreatedAt int64
}

type Message struct {
	ConversationID int64
	Seq            int64
	ID             string
	ClientMsgID    string
	SenderID       int64
	Kind           string
	Body           string
	Meta           string
	CreatedAt      int64
}
