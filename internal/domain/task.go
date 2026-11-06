package domain

import "time"

type Task struct {
	ID               int64
	ProjectID        int64
	Kind             string
	SessionID        int64
	Goal             string
	Status           string
	OwnerAgent       string
	ClaimedAt        time.Time
	NextSteps        string
	Decisions        []string
	Files            []string
	Commands         []string
	Errors           []string
	OpenQuestions    string
	SourceAgent      string
	SourcePath       string
	ManualEditedAt   time.Time
	CompletedAt      time.Time
	ArchivedAt       time.Time
	MergedIntoTaskID int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Steps            []TaskStep
	Summary          string `json:"summary,omitempty"`
	SummaryState     string `json:"summary_state,omitempty"`
	SummaryError     string `json:"summary_error,omitempty"`
}

type TaskStep struct {
	ID          int64
	TaskID      int64
	Seq         int
	Subject     string
	Description string
	ActiveForm  string
	Status      string
	SourceKey   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TaskSnippet struct {
	ID     int64
	TaskID int64
	Seq    int
	Role   string
	Kind   string
	Text   string
	Ts     time.Time
}

type TaskSession struct {
	TaskID    int64
	SessionID int64
	Role      string
	CreatedAt time.Time
	Agent     string
	Title     string
	Path      string
	UpdatedAt time.Time
}

const (
	StepPending = "pending"
	StepDoing   = "doing"
	StepDone    = "done"
	StepBlocked = "blocked"
	StepSkipped = "skipped"
)

// 任务分两种：session 是某个 agent 里的一次对话任务（扫描产出，一条会话一条），
// formal 是手工创建或把多条对话合并出来的任务。
const (
	TaskKindSession = "session"
	TaskKindFormal  = "formal"
)
