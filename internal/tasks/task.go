package tasks

type Task struct {
	ID          int    `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Status      Status `json:"status"`
	BlockedBy   []int  `json:"blockedBy"`
	Owner       string `json:"owner"`
}

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)
