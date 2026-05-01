package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type TaskManager struct {
	TaskDir string
	NextID  int
}

func NewTaskManager(taskDir string) (*TaskManager, error) {
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return nil, err
	}

	nextId := 1
	files, _ := os.ReadDir(taskDir)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if strings.HasPrefix(name, "task_") && strings.HasSuffix(name, ".json") {
			idStr := strings.TrimPrefix(name, "task_")
			idStr = strings.TrimSuffix(idStr, ".json")
			id, err := strconv.Atoi(idStr)
			if err == nil && id >= nextId {
				nextId = id + 1
			}
		}
	}

	return &TaskManager{
		TaskDir: taskDir,
		NextID:  nextId,
	}, nil
}

func (m *TaskManager) save(task *Task) error {
	filePath := filepath.Join(m.TaskDir, fmt.Sprintf("task_%d.json", task.ID))
	data, err := json.MarshalIndent(task, "", "    ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return err
	}
	return nil
}

func (m *TaskManager) load(id int) (*Task, error) {
	filePath := filepath.Join(m.TaskDir, fmt.Sprintf("task_%d.json", id))
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *TaskManager) Create(subject, desc string) (string, error) {
	if subject == "" {
		return "", errors.New("subject is required")
	}
	task := &Task{
		ID:          m.NextID,
		Subject:     subject,
		Description: desc,
		Status:      StatusPending,
		BlockedBy:   []int{},
	}
	if err := m.save(task); err != nil {
		return "", err
	}
	m.NextID++
	data, err := json.MarshalIndent(task, "", "    ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *TaskManager) Get(id int) (string, error) {
	task, err := m.load(id)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(task, "", "    ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *TaskManager) Update(id int, status Status, addBlockedBy, removeBlockBy []int) (string, error) {
	task, err := m.load(id)
	if err != nil {
		return "", err
	}
	if status != "" {
		if status == StatusCompleted {
			if err := m.clearDependency(id); err != nil {
				return "", err
			}
		}
		task.Status = status
	}
	addBlockedBy = removeDuplicates(addBlockedBy)

	if len(addBlockedBy) > 0 {
		task.BlockedBy = append(task.BlockedBy, addBlockedBy...)
	}
	if len(removeBlockBy) > 0 {
		for _, id := range removeBlockBy {
			task.BlockedBy = removeElement(task.BlockedBy, id)
		}
	}
	if err := m.save(task); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(task, "", "    ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func removeDuplicates(slice []int) []int {
	seen := make(map[int]bool)
	result := []int{}
	for _, v := range slice {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

func removeElement(slice []int, element int) []int {
	for i, v := range slice {
		if v == element {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (m *TaskManager) clearDependency(completedId int) error {
	files, err := os.ReadDir(m.TaskDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if strings.HasPrefix(name, "task_") && strings.HasSuffix(name, ".json") {
			idStr := strings.TrimPrefix(name, "task_")
			idStr = strings.TrimSuffix(idStr, ".json")
			id, err := strconv.Atoi(idStr)
			if err == nil {
				task, err := m.load(id)
				if err != nil {
					continue
				}
				task.BlockedBy = removeElement(task.BlockedBy, completedId)
				if err := m.save(task); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (m *TaskManager) List() (string, error) {
	files, err := os.ReadDir(m.TaskDir)
	if err != nil {
		return "", err
	}
	var tasks []Task
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if strings.HasPrefix(name, "task_") && strings.HasSuffix(name, ".json") {
			idStr := strings.TrimPrefix(name, "task_")
			idStr = strings.TrimSuffix(idStr, ".json")
			id, err := strconv.Atoi(idStr)
			if err == nil {
				task, err := m.load(id)
				if err != nil {
					continue
				}
				tasks = append(tasks, *task)
			}
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	var lines []string
	for _, t := range tasks {
		marker := "[?]"
		switch t.Status {
		case StatusPending:
			marker = "[ ]"
		case StatusInProgress:
			marker = "[>]"
		case StatusCompleted:
			marker = "[x]"
		}

		blocked := ""
		if len(t.BlockedBy) > 0 {
			blocked = fmt.Sprintf(" (blocked by: %v)", t.BlockedBy)
		}

		line := fmt.Sprintf("%s #%d: %s%s", marker, t.ID, t.Subject, blocked)
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return "No tasks.", nil
	}
	return strings.Join(lines, "\n"), nil
}
