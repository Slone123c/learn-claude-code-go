package team

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

var ValidMsgTypes = map[string]bool{
	"message":                true,
	"broadcast":              true,
	"shutdown_request":       true,
	"shutdown_response":      true,
	"plan_approval_response": true,
}

type Message struct {
	Type      string  `json:"type"`
	From      string  `json:"from"`
	Content   string  `json:"content"`
	Timestamp float64 `json:"timestamp"`
}

type MessageBus struct {
	dir string
	mu  sync.Mutex
}

func NewMessageBus(dir string) *MessageBus {
	os.MkdirAll(dir, 0755)
	return &MessageBus{dir: dir}
}

func (b *MessageBus) inboxPath(name string) string {
	return filepath.Join(b.dir, name+".jsonl")
}

func (b *MessageBus) Send(sender, to, content, msgType string) string {
	if !ValidMsgTypes[msgType] {
		return fmt.Sprintf("Invalid message type: %s", msgType)
	}
	msg := &Message{
		Type:      msgType,
		From:      sender,
		Content:   content,
		Timestamp: float64(time.Now().Unix()),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	line, err := json.Marshal(msg)
	if err != nil {
		return fmt.Sprintf("Failed to marshal message: %v", err)
	}

	f, err := os.OpenFile(b.inboxPath(to), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Sprintf("Failed to open inbox: %v", err)
	}
	defer f.Close()

	_, err = f.Write(append(line, '\n'))
	if err != nil {
		return fmt.Sprintf("Failed to write message: %v", err)
	}

	return fmt.Sprintf("Sent %s to %s", msgType, to)
}

func (b *MessageBus) ReadInbox(name string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	file, err := os.ReadFile(b.inboxPath(name))
	if err != nil {
		return []Message{}
	}

	lines := strings.Split(string(file), "\n")
	messages := []Message{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			fmt.Printf("Failed to unmarshal message: %v\n", err)
			continue
		}
		messages = append(messages, msg)
	}

	os.Truncate(b.inboxPath(name), 0)
	return messages
}

func (b *MessageBus) Broadcast(sender, content string, teammates []string) string {
	count := 0
	for _, name := range teammates {
		if name != sender {
			b.Send(sender, name, content, "broadcast")
			count++
		}
	}
	return fmt.Sprintf("Broadcast to %d teammates", count)
}

type Member struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type TeamConfig struct {
	TeamName string   `json:"team_name"`
	Members  []Member `json:"members"`
}

type TeammateManager struct {
	dir         string
	configPath  string
	mu          sync.Mutex
	config      TeamConfig
	client      anthropic.Client
	bus         *MessageBus
	model       string
	workingDir  string
	tools       []anthropic.ToolUnionParam
	toolHandler func(sender, toolName string, args map[string]any) string
}

func NewTeammateManager(dir string, client anthropic.Client, bus *MessageBus, model, workingDir string, tools []anthropic.ToolUnionParam, toolHandler func(sender, toolName string, args map[string]any) string) *TeammateManager {
	tm := &TeammateManager{
		dir:         dir,
		configPath:  filepath.Join(dir, "config.json"),
		client:      client,
		bus:         bus,
		model:       model,
		workingDir:  workingDir,
		tools:       tools,
		toolHandler: toolHandler,
	}
	tm.loadConfig()
	return tm
}

func (tm *TeammateManager) loadConfig() {
	data, err := os.ReadFile(tm.configPath)
	if err != nil {
		tm.config = TeamConfig{TeamName: "default"}
		return
	}
	json.Unmarshal(data, &tm.config)
}

func (tm *TeammateManager) saveConfig() {
	data, _ := json.MarshalIndent(tm.config, "", "  ")
	os.WriteFile(tm.configPath, data, 0644)
}

func (tm *TeammateManager) findMember(name string) *Member {
	for i := range tm.config.Members {
		if tm.config.Members[i].Name == name {
			return &tm.config.Members[i]
		}
	}
	return nil
}

func (tm *TeammateManager) Spawn(name, role, prompt string) string {
	tm.mu.Lock()
	member := tm.findMember(name)
	if member != nil {
		if member.Status != "idle" && member.Status != "shutdown" {
			tm.mu.Unlock()
			return fmt.Sprintf("Error: '%s' is currently %s", name, member.Status)
		}
		member.Status = "working"
		member.Role = role
	} else {
		tm.config.Members = append(tm.config.Members, Member{Name: name, Role: role, Status: "working"})
	}
	tm.saveConfig()
	tm.mu.Unlock()

	go tm.teammateLoop(name, role, prompt)
	return fmt.Sprintf("Spawned '%s' (role: %s)", name, role)
}

func (tm *TeammateManager) teammateLoop(name, role, prompt string) {
	sysPrompt := fmt.Sprintf(
		"You are '%s', role: %s, at %s. Use send_message to communicate. Complete your task.",
		name, role, tm.workingDir,
	)

	var messages []anthropic.MessageParam
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	shouldExit := false
	for range 50 {
		for _, msg := range tm.bus.ReadInbox(name) {
			data, _ := json.Marshal(msg)
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(string(data))))
		}

		resp, err := tm.client.Messages.New(context.Background(), anthropic.MessageNewParams{
			Model:     tm.model,
			MaxTokens: 8000,
			System:    []anthropic.TextBlockParam{{Text: sysPrompt}},
			Tools:     tm.tools,
			Messages:  messages,
		})
		if err != nil {
			fmt.Printf("[%s] api error: %v\n", name, err)
			break
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
			output := tm.teammateExec(name, block.Name, args)
			fmt.Printf("  [%s] %s: %s\n", name, block.Name, output[:min(len(output), 120)])
			toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, output, false))
			if block.Name == "shutdown_response" {
				if approve, _ := args["approve"].(bool); approve {
					shouldExit = true
				}
			}
		}
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
		if shouldExit {
			break
		}
	}

	tm.mu.Lock()
	if m := tm.findMember(name); m != nil {
		if shouldExit {
			m.Status = "shutdown"
		} else {
			m.Status = "idle"
		}
		tm.saveConfig()
	}
	tm.mu.Unlock()
}

func (tm *TeammateManager) teammateExec(sender, toolName string, args map[string]any) string {
	switch toolName {
	case "send_message":
		to, _ := args["to"].(string)
		content, _ := args["content"].(string)
		msgType, _ := args["msg_type"].(string)
		if msgType == "" {
			msgType = "message"
		}
		return tm.bus.Send(sender, to, content, msgType)
	case "read_inbox":
		msgs := tm.bus.ReadInbox(sender)
		data, _ := json.MarshalIndent(msgs, "", "  ")
		return string(data)
	default:
		if tm.toolHandler != nil {
			return tm.toolHandler(sender, toolName, args)
		}
		return fmt.Sprintf("Unknown tool: %s", toolName)
	}
}

func (tm *TeammateManager) ListAll() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if len(tm.config.Members) == 0 {
		return "No teammates."
	}
	lines := []string{"Team: " + tm.config.TeamName}
	for _, m := range tm.config.Members {
		lines = append(lines, fmt.Sprintf("  %s (%s): %s", m.Name, m.Role, m.Status))
	}
	return strings.Join(lines, "\n")
}

func (tm *TeammateManager) MemberNames() []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	names := make([]string, len(tm.config.Members))
	for i, m := range tm.config.Members {
		names[i] = m.Name
	}
	return names
}
