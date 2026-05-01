package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/joho/godotenv"
	"github.com/slone/learn-claude-code-go/internal/team"
)

var MODEL string

const (
	POLL_INTERVAL = 5 * time.Second
	IDLE_TIMEOUT  = 60 * time.Second
)

var (
	WorkingDir, _ = os.Getwd()
	client        anthropic.Client
	teamDir       = filepath.Join(WorkingDir, ".team")
	inboxDir      = filepath.Join(teamDir, "inbox")
	tasksDir      = filepath.Join(WorkingDir, ".tasks")
	bus           *team.MessageBus
	tm            *TeammateManager
)

type Task struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
	BlockedBy   []int  `json:"blockedBy,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

type Member struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type TeammateConfig struct {
	TeamName string   `json:"team_name"`
	Members  []Member `json:"members"`
}

func ScanUnclaimedTasks() []Task {
	files, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil
	}

	var tasks []Task
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if !strings.HasPrefix(file.Name(), "task_") || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, file.Name()))
		if err != nil {
			continue
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			continue
		}
		if task.Status == "pending" && task.Owner == "" && len(task.BlockedBy) == 0 {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func ScanAllTasks() []Task {
	files, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil
	}
	var tasks []Task
	for _, file := range files {
		if file.IsDir() || !strings.HasPrefix(file.Name(), "task_") || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, file.Name()))
		if err != nil {
			continue
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks
}

var claimLock sync.Mutex

func ClaimTask(taskID int, owner string) string {
	claimLock.Lock()
	defer claimLock.Unlock()

	path := filepath.Join(tasksDir, fmt.Sprintf("task_%d.json", taskID))
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Sprintf("Task %d not found", taskID)
	}
	if err != nil {
		return fmt.Sprintf("Failed to stat task: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Failed to read task: %v", err)
	}

	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return fmt.Sprintf("Failed to unmarshal task: %v", err)
	}

	if task.Owner != "" {
		return fmt.Sprintf("Task %d is already claimed by %s", taskID, task.Owner)
	}
	if task.Status != "pending" {
		return fmt.Sprintf("Task %d cannot be claimed because its status is %s", taskID, task.Status)
	}
	if len(task.BlockedBy) > 0 {
		return fmt.Sprintf("Task %d is blocked by other task(s) and cannot be claimed", taskID)
	}

	task.Owner = owner
	task.Status = "in_progress"

	data, err = json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Sprintf("Failed to marshal task: %v", err)
	}

	err = os.WriteFile(path, data, 0644)
	if err != nil {
		return fmt.Sprintf("Failed to write task: %v", err)
	}

	return fmt.Sprintf("Task %d claimed by %s", taskID, owner)
}

var (
	shutdownRequests = map[string]map[string]any{}
	planRequests     = map[string]map[string]any{}
	trackerLock      sync.Mutex
)

func MakeIdentityBlock(name, role, teamName string) anthropic.MessageParam {
	content := fmt.Sprintf(
		"<identity>You are '%s', role: %s, team: %s. Continue your work.</identity>",
		name,
		role,
		teamName,
	)
	return anthropic.NewUserMessage(anthropic.NewTextBlock(content))
}

type TeammateManager struct {
	dir        string
	configPath string
	config     TeammateConfig
	mu         sync.RWMutex
	threads    map[string]bool
}

func NewTeammateManager(dir string) (*TeammateManager, error) {
	os.MkdirAll(dir, 0755)
	tm := &TeammateManager{
		dir:        dir,
		configPath: filepath.Join(dir, "config.json"),
		threads:    make(map[string]bool),
	}
	tm.LoadConfig()
	return tm, nil
}

func (tm *TeammateManager) LoadConfig() error {
	data, err := os.ReadFile(tm.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			tm.config = TeammateConfig{
				TeamName: "default",
				Members:  []Member{},
			}
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &tm.config)
}

func (tm *TeammateManager) SaveConfig() error {
	data, err := json.MarshalIndent(tm.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tm.configPath, data, 0644)
}

func (tm *TeammateManager) FindMember(name string) *Member {
	for i := range tm.config.Members {
		if tm.config.Members[i].Name == name {
			return &tm.config.Members[i]
		}
	}
	return nil
}

func (tm *TeammateManager) SetStatus(name, status string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	member := tm.FindMember(name)
	if member == nil {
		return errors.New("member not found")
	}
	member.Status = status
	return tm.SaveConfig()
}

func (tm *TeammateManager) Spawn(name, role, prompt string) (string, error) {
	tm.mu.Lock()

	member := tm.FindMember(name)
	if member != nil {
		if member.Status != "idle" && member.Status != "shutdown" {
			tm.mu.Unlock()
			return "", fmt.Errorf("Error: '%s' is currently %s", name, member.Status)
		}
		member.Status = "working"
		member.Role = role
	} else {
		tm.config.Members = append(tm.config.Members, Member{
			Name:   name,
			Role:   role,
			Status: "working",
		})
	}

	tm.SaveConfig()

	if tm.threads == nil {
		tm.threads = make(map[string]bool)
	}
	tm.threads[name] = true
	tm.mu.Unlock()

	go tm.loop(name, role, prompt)

	return fmt.Sprintf("Spawned '%s' (role: %s)", name, role), nil
}

func (tm *TeammateManager) loop(name, role, prompt string) {
	teamName := tm.config.TeamName
	sysPrompt := fmt.Sprintf("You are '%s', role: %s, team: %s, at %s. Use idle tool when you have no more work. You will auto-claim new tasks.",
		name, role, teamName, WorkingDir)

	var messages []anthropic.MessageParam
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	for {
		idleRequested := false
		for i := 0; i < 50; i++ {
			inbox := bus.ReadInbox(name)
			for _, msg := range inbox {
				if msg.Type == "shutdown_request" {
					tm.SetStatus(name, "shutdown")
					return
				}
				msgJSON, _ := json.Marshal(msg)
				messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(string(msgJSON))))
			}

			if len(messages) > 20 {
				compacted := []anthropic.MessageParam{messages[0]}
				compacted = append(compacted, MakeIdentityBlock(name, role, teamName))
				compacted = append(compacted, messages[len(messages)-10:]...)
				messages = compacted
			}

			resp, err := client.Messages.New(
				context.Background(),
				anthropic.MessageNewParams{
					Model:     MODEL,
					MaxTokens: 8000,
					System:    []anthropic.TextBlockParam{{Text: sysPrompt}},
					Tools:     tm.tools(),
					Messages:  messages,
				})
			if err != nil {
				fmt.Printf("[%s] API Error: %v\n", name, err)
				tm.SetStatus(name, "idle")
				return
			}

			var assistantBlocks []anthropic.ContentBlockParamUnion
			for _, block := range resp.Content {
				switch block.Type {
				case "text":
					assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(block.Text))
				case "tool_use":
					assistantBlocks = append(assistantBlocks, anthropic.NewToolUseBlock(block.ID, block.Input, block.Name))
				}
			}
			messages = append(messages, anthropic.NewAssistantMessage(assistantBlocks...))

			if resp.StopReason != anthropic.StopReasonToolUse {
				break
			}

			var toolResults []anthropic.ContentBlockParamUnion
			for _, block := range resp.Content {
				if block.Type != "tool_use" {
					continue
				}
				var args map[string]any
				json.Unmarshal(block.Input, &args)
				output := ""

				switch block.Name {
				case "idle":
					idleRequested = true
					output = "Entering idle phase. Will poll for new tasks."
				case "bash":
					cmd, _ := args["command"].(string)
					output, _ = RunBash(cmd)
				case "read_file":
					path, _ := args["path"].(string)
					limit := 0
					if l, ok := args["limit"].(float64); ok {
						limit = int(l)
					}
					output, _ = RunReadFile(path, limit)
				case "write_file":
					path, _ := args["path"].(string)
					content, _ := args["content"].(string)
					output, _ = RunWriteFile(path, content)
				case "edit_file":
					path, _ := args["path"].(string)
					oldText, _ := args["old_text"].(string)
					newText, _ := args["new_text"].(string)
					safePath, err := getSafePath(path)
					if err != nil {
						output = err.Error()
					} else {
						contentBytes, err := os.ReadFile(safePath)
						if err != nil {
							output = fmt.Sprintf("Error: %v", err)
						} else if !strings.Contains(string(contentBytes), oldText) {
							output = fmt.Sprintf("Error: Text not found in %s", path)
						} else {
							newContent := strings.Replace(string(contentBytes), oldText, newText, 1)
							os.WriteFile(safePath, []byte(newContent), 0644)
							output = fmt.Sprintf("Edited %s", path)
						}
					}
				case "send_message":
					to, _ := args["to"].(string)
					content, _ := args["content"].(string)
					msgType, _ := args["msg_type"].(string)
					if msgType == "" {
						msgType = "message"
					}
					output = bus.Send(name, to, content, msgType)
				case "read_inbox":
					msgs := bus.ReadInbox(name)
					data, _ := json.MarshalIndent(msgs, "", "  ")
					output = string(data)
				case "claim_task":
					taskID, _ := args["task_id"].(float64)
					output = ClaimTask(int(taskID), name)
				case "shutdown_response":
					reqID, _ := args["request_id"].(string)
					approve, _ := args["approve"].(bool)
					reason, _ := args["reason"].(string)
					trackerLock.Lock()
					if r, ok := shutdownRequests[reqID]; ok {
						if approve {
							r["status"] = "approved"
						} else {
							r["status"] = "rejected"
						}
					}
					trackerLock.Unlock()
					payload, _ := json.Marshal(map[string]any{
						"message":    reason,
						"request_id": reqID,
						"approve":    approve,
					})
					bus.Send(name, "lead", string(payload), "shutdown_response")
					if approve {
						output = "Shutdown approved"
					} else {
						output = "Shutdown rejected"
					}
				case "plan_approval":
					plan, _ := args["plan"].(string)
					reqID := fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
					trackerLock.Lock()
					planRequests[reqID] = map[string]any{"from": name, "plan": plan, "status": "pending"}
					trackerLock.Unlock()
					payload, _ := json.Marshal(map[string]any{
						"request_id": reqID,
						"plan":       plan,
					})
					bus.Send(name, "lead", string(payload), "plan_approval_response")
					output = fmt.Sprintf("Plan submitted (request_id=%s). Waiting for approval.", reqID)
				default:
					output = fmt.Sprintf("Unknown tool: %s", block.Name)
				}

				outputLog := output
				if len(outputLog) > 120 {
					outputLog = outputLog[:120] + "..."
				}
				fmt.Printf("  [%s] %s: %s\n", name, block.Name, outputLog)
				toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, output, false))
			}
			messages = append(messages, anthropic.NewUserMessage(toolResults...))

			if idleRequested {
				break
			}
		}

		member := tm.FindMember(name)
		if member != nil && member.Status == "shutdown" {
			break
		}

		tm.SetStatus(name, "idle")

		foundWork := false
		endTime := time.Now().Add(IDLE_TIMEOUT)

		for time.Now().Before(endTime) {
			inbox := bus.ReadInbox(name)
			if len(inbox) > 0 {
				for _, msg := range inbox {
					if msg.Type == "shutdown_request" {
						tm.SetStatus(name, "shutdown")
						return
					}
					msgJSON, _ := json.Marshal(msg)
					messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(string(msgJSON))))
				}
				foundWork = true
				break
			}

			tasks := ScanUnclaimedTasks()
			if len(tasks) > 0 {
				task := tasks[0]
				result := ClaimTask(task.ID, name)
				if strings.HasPrefix(result, "Error:") {
					time.Sleep(POLL_INTERVAL)
					continue
				}
				taskPrompt := fmt.Sprintf("<auto-claimed>Task #%d: %s\n%s</auto-claimed>",
					task.ID, task.Subject, task.Description)
				if len(messages) <= 3 {
					injected := []anthropic.MessageParam{
						MakeIdentityBlock(name, role, teamName),
						anthropic.NewAssistantMessage(anthropic.NewTextBlock(fmt.Sprintf("I am %s. Continuing.", name))),
					}
					messages = append(injected, messages...)
				}
				messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(taskPrompt)))
				messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(fmt.Sprintf("Claimed task #%d. Working on it.", task.ID))))
				foundWork = true
				break
			}

			time.Sleep(POLL_INTERVAL)
		}

		if !foundWork {
			tm.SetStatus(name, "shutdown")
			break
		}

		tm.SetStatus(name, "working")
	}
}

var ToolBash = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "bash",
		Description: anthropic.String("Run a shell command."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"command": map[string]any{"type": "string"},
			},
			Required: []string{"command"},
		},
	},
}

var ToolReadFile = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "read_file",
		Description: anthropic.String("Read file contents."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
			Required: []string{"path"},
		},
	},
}

var ToolWriteFile = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "write_file",
		Description: anthropic.String("Write content to file."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			Required: []string{"path", "content"},
		},
	},
}

var ToolEditFile = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "edit_file",
		Description: anthropic.String("Replace exact text in file."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":     map[string]any{"type": "string"},
				"old_text": map[string]any{"type": "string"},
				"new_text": map[string]any{"type": "string"},
			},
			Required: []string{"path", "old_text", "new_text"},
		},
	},
}

var ToolSendMessage = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "send_message",
		Description: anthropic.String("Send message to a teammate."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"to":      map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
				"msg_type": map[string]any{
					"type": "string",
					"enum": []string{
						"message",
						"broadcast",
						"shutdown_request",
						"shutdown_response",
						"plan_approval_response",
					},
				},
			},
			Required: []string{"to", "content"},
		},
	},
}

var ToolReadInbox = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "read_inbox",
		Description: anthropic.String("Read and drain your inbox."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
}

var ToolTeammateShutdownResponse = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "shutdown_response",
		Description: anthropic.String("Respond to a shutdown request. Approve to shut down, reject to keep working."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"request_id": map[string]any{"type": "string"},
				"approve":    map[string]any{"type": "boolean"},
				"reason":     map[string]any{"type": "string"},
			},
			Required: []string{"request_id", "approve"},
		},
	},
}

var ToolTeammatePlanApproval = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "plan_approval",
		Description: anthropic.String("Submit a plan for lead approval. Provide plan text."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"plan": map[string]any{"type": "string"},
			},
			Required: []string{"plan"},
		},
	},
}

var ToolIdle = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "idle",
		Description: anthropic.String("Signal that you have no more work. Enters idle polling phase."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
}

var ToolClaimTask = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "claim_task",
		Description: anthropic.String("Claim a task from the task board by ID."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"task_id": map[string]any{"type": "integer"},
			},
			Required: []string{"task_id"},
		},
	},
}

func (tm *TeammateManager) tools() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{
		ToolBash,
		ToolReadFile,
		ToolWriteFile,
		ToolEditFile,
		ToolSendMessage,
		ToolReadInbox,
		ToolTeammateShutdownResponse,
		ToolTeammatePlanApproval,
		ToolIdle,
		ToolClaimTask,
	}
}

var ToolSpawnTeammate = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "spawn_teammate",
		Description: anthropic.String("Spawn an autonomous teammate."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"name":   map[string]any{"type": "string"},
				"role":   map[string]any{"type": "string"},
				"prompt": map[string]any{"type": "string"},
			},
			Required: []string{"name", "role", "prompt"},
		},
	},
}

var ToolListTeammates = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "list_teammates",
		Description: anthropic.String("List all teammates."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
}

var ToolBroadcast = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "broadcast",
		Description: anthropic.String("Send a message to all teammates."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"content": map[string]any{"type": "string"},
			},
			Required: []string{"content"},
		},
	},
}

var ToolShutdownRequest = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "shutdown_request",
		Description: anthropic.String("Request a teammate to shut down."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"teammate": map[string]any{"type": "string"},
			},
			Required: []string{"teammate"},
		},
	},
}

var ToolPlanApproval = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "plan_approval",
		Description: anthropic.String("Approve or reject a teammate's plan."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"request_id": map[string]any{"type": "string"},
				"approve":    map[string]any{"type": "boolean"},
				"feedback":   map[string]any{"type": "string"},
			},
			Required: []string{"request_id", "approve"},
		},
	},
}

var ToolLeadShutdownResponse = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "shutdown_response",
		Description: anthropic.String("Check shutdown request status."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"request_id": map[string]any{"type": "string"},
			},
			Required: []string{"request_id"},
		},
	},
}

func leadTools() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{
		ToolBash,
		ToolReadFile,
		ToolWriteFile,
		ToolEditFile,
		ToolSpawnTeammate,
		ToolListTeammates,
		ToolSendMessage,
		ToolReadInbox,
		ToolBroadcast,
		ToolShutdownRequest,
		ToolLeadShutdownResponse,
		ToolPlanApproval,
		ToolIdle,
		ToolClaimTask,
	}
}

func getSafePath(p string) (string, error) {
	var absPath string
	if filepath.IsAbs(p) {
		absPath = filepath.Clean(p)
	} else {
		absPath = filepath.Join(WorkingDir, p)
		absPath = filepath.Clean(absPath)
	}
	if !strings.HasPrefix(absPath, WorkingDir) {
		return "", errors.New("Error: Path escapes workspace")
	}
	return absPath, nil
}

func RunBash(command string) (string, error) {
	fmt.Printf("\033[33m$ %s\033[0m\n", command)
	dangerous := []string{"rm -rf /", "sudo", "shutdown", "reboot", "> /dev/"}
	for _, str := range dangerous {
		if strings.Contains(command, str) {
			return "Error: Dangerous command blocked", nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = WorkingDir
	res, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "Error: Timeout (120s)", nil
		}
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(res)), nil
	}
	output := string(res)
	switch {
	case output == "":
		return "(no output)", nil
	case len(output) > 50000:
		return output[:50000], nil
	default:
		return output, nil
	}
}

func RunReadFile(path string, limit int) (string, error) {
	safePath, err := getSafePath(path)
	if err != nil {
		return "", err
	}
	fmt.Printf("\033[33m> read_file: %s\033[0m\n", path)
	content, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	if limit > 0 && len(lines) > limit {
		remaining := len(lines) - limit
		lines = lines[:limit]
		lines = append(lines, fmt.Sprintf("... %d more lines...", remaining))
	}
	result := strings.Join(lines, "\n")
	if len(result) > 50000 {
		result = result[:50000]
	}
	return result, nil
}

func RunWriteFile(path, content string) (string, error) {
	safePath, err := getSafePath(path)
	if err != nil {
		return "", err
	}
	fmt.Printf("\033[33m> write_file: %s\033[0m\n", path)
	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), nil
}

func agentLoop(messages []anthropic.MessageParam, tm *TeammateManager) {
	SYSTEM := fmt.Sprintf("You are a team lead at %s. Teammates are autonomous -- they find work themselves.", WorkingDir)

	for {
		inbox := bus.ReadInbox("lead")
		if len(inbox) > 0 {
			inboxJSON, _ := json.MarshalIndent(inbox, "", "  ")
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(fmt.Sprintf("<inbox>%s</inbox>", string(inboxJSON)))))
		}

		resp, err := client.Messages.New(
			context.Background(),
			anthropic.MessageNewParams{
				Model:     MODEL,
				MaxTokens: 8000,
				System:    []anthropic.TextBlockParam{{Text: SYSTEM}},
				Tools:     leadTools(),
				Messages:  messages,
			})
		if err != nil {
			fmt.Printf("Lead API Error: %v\n", err)
			break
		}

		var assistantBlocks []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(block.Text))
				fmt.Println(block.Text)
			case "tool_use":
				assistantBlocks = append(assistantBlocks, anthropic.NewToolUseBlock(block.ID, block.Input, block.Name))
			}
		}
		messages = append(messages, anthropic.NewAssistantMessage(assistantBlocks...))

		if resp.StopReason != anthropic.StopReasonToolUse {
			break
		}

		var toolResults []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}

			var args map[string]any
			json.Unmarshal(block.Input, &args)
			output := ""

			switch block.Name {
			case "bash":
				cmd, _ := args["command"].(string)
				output, _ = RunBash(cmd)
			case "read_file":
				path, _ := args["path"].(string)
				limit := 0
				if l, ok := args["limit"].(float64); ok {
					limit = int(l)
				}
				output, _ = RunReadFile(path, limit)
			case "write_file":
				path, _ := args["path"].(string)
				content, _ := args["content"].(string)
				output, _ = RunWriteFile(path, content)
			case "edit_file":
				path, _ := args["path"].(string)
				oldText, _ := args["old_text"].(string)
				newText, _ := args["new_text"].(string)
				safePath, err := getSafePath(path)
				if err != nil {
					output = err.Error()
				} else {
					contentBytes, err := os.ReadFile(safePath)
					if err != nil {
						output = fmt.Sprintf("Error: %v", err)
					} else if !strings.Contains(string(contentBytes), oldText) {
						output = fmt.Sprintf("Error: Text not found in %s", path)
					} else {
						newContent := strings.Replace(string(contentBytes), oldText, newText, 1)
						os.WriteFile(safePath, []byte(newContent), 0644)
						output = fmt.Sprintf("Edited %s", path)
					}
				}
			case "spawn_teammate":
				name, _ := args["name"].(string)
				role, _ := args["role"].(string)
				prompt, _ := args["prompt"].(string)
				output, _ = tm.Spawn(name, role, prompt)
			case "list_teammates":
				for _, member := range tm.config.Members {
					output += fmt.Sprintf("  %s (%s): %s\n", member.Name, member.Role, member.Status)
				}
				if output == "" {
					output = "No teammates."
				}
			case "send_message":
				to, _ := args["to"].(string)
				content, _ := args["content"].(string)
				msgType, _ := args["msg_type"].(string)
				if msgType == "" {
					msgType = "message"
				}
				output = bus.Send("lead", to, content, msgType)
			case "read_inbox":
				msgs := bus.ReadInbox("lead")
				data, _ := json.MarshalIndent(msgs, "", "  ")
				output = string(data)
			case "broadcast":
				content, _ := args["content"].(string)
				output = bus.Broadcast("lead", content, tm.member_names())
			case "shutdown_request":
				teammate, _ := args["teammate"].(string)
				reqID := fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
				trackerLock.Lock()
				shutdownRequests[reqID] = map[string]any{"target": teammate, "status": "pending"}
				trackerLock.Unlock()
				output = fmt.Sprintf("Shutdown request %s sent to '%s'", reqID, teammate)
				payload, _ := json.Marshal(map[string]any{
					"message":    "Please shut down gracefully.",
					"request_id": reqID,
				})
				bus.Send("lead", teammate, string(payload), "shutdown_request")
			case "shutdown_response":
				requestID, _ := args["request_id"].(string)
				trackerLock.Lock()
				req, ok := shutdownRequests[requestID]
				trackerLock.Unlock()
				if !ok {
					output = fmt.Sprintf("Error: Unknown shutdown request_id '%s'", requestID)
				} else {
					data, _ := json.Marshal(req)
					output = string(data)
				}
			case "plan_approval":
				requestID, _ := args["request_id"].(string)
				approve, _ := args["approve"].(bool)
				feedback, _ := args["feedback"].(string)
				trackerLock.Lock()
				req, ok := planRequests[requestID]
				if ok {
					if approve {
						req["status"] = "approved"
					} else {
						req["status"] = "rejected"
					}
				}
				trackerLock.Unlock()
				if !ok {
					output = fmt.Sprintf("Error: Unknown plan request_id '%s'", requestID)
				} else {
					from, _ := req["from"].(string)
					payload, _ := json.Marshal(map[string]any{
						"request_id": requestID,
						"approve":    approve,
						"feedback":   feedback,
					})
					bus.Send("lead", from, string(payload), "plan_approval_response")
					status := "approved"
					if !approve {
						status = "rejected"
					}
					output = fmt.Sprintf("Plan %s for '%s'", status, from)
				}
			case "idle":
				output = "Lead does not idle."
			case "claim_task":
				taskID, _ := args["task_id"].(float64)
				output = ClaimTask(int(taskID), "lead")
			default:
				output = fmt.Sprintf("Unknown tool: %s", block.Name)
			}

			outputLog := output
			if len(outputLog) > 120 {
				outputLog = outputLog[:120] + "..."
			}
			fmt.Printf("> %s: %s\n", block.Name, outputLog)
			toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, output, false))
		}

		if len(toolResults) > 0 {
			messages = append(messages, anthropic.NewUserMessage(toolResults...))
		}
	}
}

func (tm *TeammateManager) member_names() []string {
	var names []string
	for _, m := range tm.config.Members {
		names = append(names, m.Name)
	}
	return names
}

func main() {
	for _, p := range []string{"../../.env", "../.env", ".env"} {
		if err := godotenv.Load(p); err == nil {
			fmt.Printf("Loaded config from: %s\n", p)
			break
		}
	}
	MODEL = os.Getenv("MODEL")
	if MODEL == "" {
		fmt.Fprintln(os.Stderr, "MODEL is required. Set MODEL in .env or environment.")
		os.Exit(1)
	}

	client = anthropic.NewClient()

	os.MkdirAll(inboxDir, 0755)
	os.MkdirAll(tasksDir, 0755)

	bus = team.NewMessageBus(inboxDir)
	var err error
	tm, err = NewTeammateManager(teamDir)
	if err != nil {
		fmt.Println("Failed to initialize TeammateManager:", err)
		return
	}

	fmt.Println("s11 - Autonomous Agents framework initialized")
	fmt.Println("Team directory:", teamDir)

	var messages []anthropic.MessageParam
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("\033[36ms11 >> \033[0m")
		input, ioErr := reader.ReadString('\n')
		if ioErr != nil && ioErr != io.EOF {
			break
		}
		trimmed := strings.TrimSpace(input)

		lower := strings.ToLower(trimmed)
		if lower == "exit" || lower == "q" || lower == "quit" || trimmed == "" {
			break
		}

		if trimmed == "/team" {
			fmt.Println(tm.config.TeamName)
			for _, m := range tm.config.Members {
				fmt.Printf("  %s (%s): %s\n", m.Name, m.Role, m.Status)
			}
			continue
		}
		if trimmed == "/inbox" {
			msgs := bus.ReadInbox("lead")
			data, _ := json.MarshalIndent(msgs, "", "  ")
			fmt.Println(string(data))
			continue
		}
		if trimmed == "/tasks" {
			tasks := ScanAllTasks()
			for _, task := range tasks {
				marker := "[ ]"
				if task.Status == "in_progress" {
					marker = "[>]"
				} else if task.Status == "completed" {
					marker = "[x]"
				}
				owner := ""
				if task.Owner != "" {
					owner = " @" + task.Owner
				}
				fmt.Printf("  %s #%d: %s%s\n", marker, task.ID, task.Subject, owner)
			}
			continue
		}

		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(trimmed)))
		agentLoop(messages, tm)

		if ioErr == io.EOF {
			break
		}
	}
}
