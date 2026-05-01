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
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"github.com/slone/learn-claude-code-go/internal/todo"
)

var MODEL string

var WorkingDir, _ = os.Getwd()
var SYSTEM_PROMPT = fmt.Sprintf("You are a coding agent at %s. Use tools to solve tasks. Act, don't explain.", WorkingDir)
var TODO = &todo.TodoManager{}

var TOOLS = []anthropic.ToolUnionParam{
	{OfTool: &anthropic.ToolParam{
		Name: "bash", Description: anthropic.String("Run a shell command."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"command": map[string]any{"type": "string"}},
			Required:   []string{"command"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "read_file", Description: anthropic.String("Read file contents."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"path": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}},
			Required:   []string{"path"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "write_file", Description: anthropic.String("Write content to file."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}},
			Required:   []string{"path", "content"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "edit_file", Description: anthropic.String("Replace exact text in file."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"path": map[string]any{"type": "string"}, "old_text": map[string]any{"type": "string"}, "new_text": map[string]any{"type": "string"}},
			Required:   []string{"path", "old_text", "new_text"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "todo", Description: anthropic.String("Update task list. Track progress on multi-step tasks."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"items": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"},
							"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						},
						"required": []string{"id", "text", "status"},
					},
				},
			},
			Required: []string{"items"},
		},
	}},
}

type ToolHandler func(input map[string]any) string

var TOOL_HANDLERS = map[string]ToolHandler{
	"bash": runBash, "read_file": runRead, "write_file": runWrite, "edit_file": runEdit, "todo": runTodo,
}

func getSafePath(p string) (string, error) {
	var absPath string
	if filepath.IsAbs(p) {
		absPath = filepath.Clean(p)
	} else {
		absPath = filepath.Clean(filepath.Join(WorkingDir, p))
	}
	if !strings.HasPrefix(absPath, WorkingDir) {
		return "", errors.New("Error: Path escapes workspace")
	}
	return absPath, nil
}

func runBash(input map[string]any) string {
	command, ok := input["command"].(string)
	if !ok {
		return "Error: command is not a string"
	}
	fmt.Printf("\033[33m$ %s\033[0m\n", command)
	for _, d := range []string{"rm -rf /", "sudo", "shutdown", "reboot", "> /dev/"} {
		if strings.Contains(command, d) {
			return "Error: Dangerous command blocked"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = WorkingDir
	res, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "Error: Timeout (120s)"
		}
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(res))
	}
	output := string(res)
	if output == "" {
		return "(no output)"
	}
	if len(output) > 50000 {
		return output[:50000]
	}
	return output
}

func runRead(input map[string]any) string {
	path, _ := input["path"].(string)
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> read_file: %s\033[0m\n", path)
	limit := 0
	if f, ok := input["limit"].(float64); ok {
		limit = int(f)
	}
	content, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	lines := strings.Split(string(content), "\n")
	if limit > 0 && len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("... %d more lines...", len(lines)-limit))
	}
	result := strings.Join(lines, "\n")
	if len(result) > 50000 {
		result = result[:50000]
	}
	return result
}

func runWrite(input map[string]any) string {
	path, _ := input["path"].(string)
	content, _ := input["content"].(string)
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> write_file: %s\033[0m\n", path)
	os.MkdirAll(filepath.Dir(safePath), 0755)
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
}

func runEdit(input map[string]any) string {
	path, _ := input["path"].(string)
	oldText, _ := input["old_text"].(string)
	newText, _ := input["new_text"].(string)
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> edit_file: %s\033[0m\n", path)
	c, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if !strings.Contains(string(c), oldText) {
		return fmt.Sprintf("Error: Text not found in %s", path)
	}
	os.WriteFile(safePath, []byte(strings.Replace(string(c), oldText, newText, 1)), 0644)
	return fmt.Sprintf("Edited %s", path)
}

func runTodo(input map[string]any) string {
	itemsRaw, ok := input["items"].([]any)
	if !ok {
		return "Error: items must be an array"
	}
	var items []todo.TodoItem
	for _, raw := range itemsRaw {
		m, _ := raw.(map[string]any)
		id, _ := m["id"].(string)
		text, _ := m["text"].(string)
		status, _ := m["status"].(string)
		items = append(items, todo.TodoItem{ID: id, Text: text, Status: todo.Status(status)})
	}
	fmt.Printf("\033[33m> todo: %d items\033[0m\n", len(items))
	result, err := TODO.Update(items)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return result
}

func isQuit(s string) bool {
	l := strings.ToLower(s)
	return l == "exit" || l == "q" || l == "quit" || s == ""
}

func printLastReply(resp *anthropic.Message) {
	for _, b := range resp.Content {
		if b.Type == "text" && b.Text != "" {
			fmt.Println(b.Text)
		}
	}
}

func main() {
	for _, p := range []string{"../../.env", "../.env", ".env"} {
		if godotenv.Load(p) == nil {
			break
		}
	}
	MODEL = os.Getenv("MODEL")
	if MODEL == "" {
		fmt.Fprintln(os.Stderr, "MODEL is required. Set MODEL in .env or environment.")
		os.Exit(1)
	}
	client := anthropic.NewClient()
	var messages []anthropic.MessageParam
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("s03 >> : ")
		input, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			logrus.Error(err)
			break
		}
		trimmed := strings.TrimSpace(input)
		if isQuit(trimmed) {
			break
		}
		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(trimmed)))
		resp, loopErr := agentLoop(&messages, client)
		if loopErr != nil {
			logrus.Error(loopErr)
		} else {
			printLastReply(resp)
		}
		if err == io.EOF {
			break
		}
	}
}

func agentLoop(messages *[]anthropic.MessageParam, client anthropic.Client) (*anthropic.Message, error) {
	roundsSinceTodo := 0
	for {
		resp, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
			Model: MODEL, MaxTokens: 8000,
			System: []anthropic.TextBlockParam{{Text: SYSTEM_PROMPT}},
			Tools:  TOOLS, Messages: *messages,
		})
		if err != nil {
			return nil, err
		}
		var ab []anthropic.ContentBlockParamUnion
		for _, b := range resp.Content {
			switch b.Type {
			case "text":
				ab = append(ab, anthropic.NewTextBlock(b.Text))
			case "tool_use":
				ab = append(ab, anthropic.NewToolUseBlock(b.ID, b.Input, b.Name))
			}
		}
		*messages = append(*messages, anthropic.NewAssistantMessage(ab...))
		if resp.StopReason != anthropic.StopReasonToolUse {
			return resp, nil
		}
		var tr []anthropic.ContentBlockParamUnion
		usedTodo := false
		for _, b := range resp.Content {
			if b.Type != "tool_use" {
				continue
			}
			var input map[string]any
			if err := json.Unmarshal(b.Input, &input); err != nil {
				return nil, fmt.Errorf("decode tool input for %s: %w", b.Name, err)
			}
			handler, ok := TOOL_HANDLERS[b.Name]
			var output string
			if !ok {
				output = fmt.Sprintf("Unknown tool: %s", b.Name)
			} else {
				output = handler(input)
			}
			if len(output) > 200 && b.Name != "todo" {
				fmt.Println(output[:200])
			} else {
				fmt.Println(output)
			}
			tr = append(tr, anthropic.NewToolResultBlock(b.ID, output, false))
			if b.Name == "todo" {
				usedTodo = true
			}
		}
		if len(tr) == 0 {
			return nil, fmt.Errorf("stop_reason was tool_use but no tool_use blocks were returned")
		}
		if usedTodo {
			roundsSinceTodo = 0
		} else {
			roundsSinceTodo++
		}
		if roundsSinceTodo >= 3 {
			tr = append(tr, anthropic.NewTextBlock("<reminder>Update your todos.</reminder>"))
		}
		*messages = append(*messages, anthropic.NewUserMessage(tr...))
	}
}
