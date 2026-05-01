package background

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type BackgroundManager struct {
	Tasks             map[string]*Task
	NotificationQueue []string
	lock              sync.RWMutex
}

type Task struct {
	ID      string
	Status  string
	Result  string
	Command string
}

func (t *Task) String() string {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return string(b)
}

func NewBackgroundManager() *BackgroundManager {
	return &BackgroundManager{
		Tasks:             make(map[string]*Task),
		NotificationQueue: make([]string, 0),
	}
}

func (bm *BackgroundManager) Run(command string) string {
	bm.lock.Lock()
	defer bm.lock.Unlock()

	taskID := uuid.New().String()

	bm.Tasks[taskID] = &Task{
		ID:      taskID,
		Status:  "running",
		Command: command,
	}

	go bm.execute(taskID, command)

	return fmt.Sprintf("Background task %s started: %s", taskID, command)
}

func (bm *BackgroundManager) execute(taskID, command string) {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second*30)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	output, err := cmd.CombinedOutput()

	resultStr := string(output)
	if err != nil {
		resultStr = fmt.Sprintf("Error: %v\n%s", err, resultStr)
	}

	bm.lock.Lock()
	defer bm.lock.Unlock()

	bm.Tasks[taskID].Status = "completed"
	bm.Tasks[taskID].Result = resultStr

	truncatedResult := resultStr
	if len(truncatedResult) > 500 {
		truncatedResult = truncatedResult[:500]
	}

	msg := &NotificationMsg{
		TaskID:  taskID,
		Status:  "completed",
		Command: command,
		Result:  truncatedResult,
	}
	bm.NotificationQueue = append(bm.NotificationQueue, msg.String())
}

type NotificationMsg struct {
	TaskID  string
	Status  string
	Command string
	Result  string
}

func (r *NotificationMsg) String() string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return string(b)
}

func (bm *BackgroundManager) Check(taskID string) string {
	bm.lock.RLock()
	defer bm.lock.RUnlock()

	if taskID == "" {
		if len(bm.Tasks) == 0 {
			return "No background tasks."
		}
		var lines []string
		for id, t := range bm.Tasks {
			lines = append(lines, fmt.Sprintf("%s: [%s] %s", id, t.Status, t.Command))
		}
		return "Background Tasks:\n" + strings.Join(lines, "\n")
	}

	if task, ok := bm.Tasks[taskID]; ok {
		return task.String()
	}
	return fmt.Sprintf("Error: task %s not found", taskID)
}

func (bm *BackgroundManager) DrainNotifications() []string {
	bm.lock.Lock()
	defer bm.lock.Unlock()

	notifs := bm.NotificationQueue
	bm.NotificationQueue = nil
	return notifs
}
