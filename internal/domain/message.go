package domain

import "time"

// SessionMessage 是完整对话表里的一条消息：文本全文、工具摘要或报错结果。
type SessionMessage struct {
	ID        int64
	SessionID int64
	Seq       int
	Role      string
	Kind      string
	Text      string
	Tool      string
	Files     []string
	Ts        time.Time
	Truncated bool
}

// MessageHit 是全文检索的一条命中，带所属会话与任务。
type MessageHit struct {
	SessionMessage
	Agent  string
	Title  string
	TaskID int64
}
