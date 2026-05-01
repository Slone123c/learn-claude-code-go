package todo

import (
	"errors"
	"fmt"
	"strings"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

type TodoItem struct {
	ID     string
	Text   string
	Status Status
}

type TodoManager struct {
	InProgressCount int
	TodoItemList    []TodoItem
}

func (tm *TodoManager) Update(items []TodoItem) (string, error) {
	if len(items) > 20 {
		return "", errors.New("Max 20 todos allowed")
	}
	inProgressCount := 0
	validated := []TodoItem{}

	for _, v := range items {
		text := v.Text
		status := v.Status
		itemID := v.ID
		if text == "" {
			return "", fmt.Errorf("item %s :text cannot be empty", itemID)
		}
		if status != StatusPending && status != StatusInProgress && status != StatusCompleted {
			return "", fmt.Errorf("item %s :status is invalid", itemID)
		}
		if status == StatusInProgress {
			inProgressCount++
		}
		validated = append(validated, v)
	}
	if inProgressCount > 1 {
		return "", errors.New("Only one task can be in_progress at a time")
	}
	tm.TodoItemList = validated
	tm.InProgressCount = inProgressCount
	return tm.Render(), nil
}

func (tm *TodoManager) Render() string {
	if len(tm.TodoItemList) == 0 {
		return "No todos"

	}
	done := 0
	lines := []string{}
	for _, v := range tm.TodoItemList {
		marker := ""
		switch v.Status {
		case StatusPending:
			marker = "[ ]"
		case StatusInProgress:
			marker = "[>]"
		case StatusCompleted:
			marker = "[x]"
			done++
		}
		lines = append(lines, fmt.Sprintf("%s #%s: %s", marker, v.ID, v.Text))
	}
	stats := fmt.Sprintf("\n(%d/%d completed)", done, len(tm.TodoItemList))
	return strings.Join(lines, "\n") + stats
}
