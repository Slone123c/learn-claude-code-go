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
	"github.com/slone/learn-claude-code-go/internal/tasks"
)

var MODEL string

var WorkingDir, _ = os.Getwd()
var SYSTEM_PROMPT = fmt.Sprintf("You are a coding agent at %s. Use task tools to plan and track work.", WorkingDir)

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
		Name: "task_create", Description: anthropic.String("Create a new task."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"subject": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}},
			Required:   []string{"subject"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "task_update", Description: anthropic.String("Update a task's status or dependencies."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"task_id":         map[string]any{"type": "integer"},
				"status":          map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
				"addBlockedBy":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"removeBlockedBy": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			Required: []string{"task_id"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "task_list", Description: anthropic.String("List all tasks with status summary."),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "task_get", Description: anthropic.String("Get full details of a task by ID."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"task_id": map[string]any{"type": "integer"}},
			Required:   []string{"task_id"},
		},
	}},
}

type ToolHandler func(input map[string]any) string

var taskManager *tasks.TaskManager

var TOOL_HANDLERS map[string]ToolHandler

func initHandlers() {
	TOOL_HANDLERS = map[string]ToolHandler{
		"bash": runBash, "read_file": runRead, "write_file": runWrite, "edit_file": runEdit,
		"task_create": runTaskCreate, "task_update": runTaskUpdate, "task_list": runTaskList, "task_get": runTaskGet,
	}
}

func runTaskCreate(input map[string]any) string {
	subject, _ := input["subject"].(string)
	desc, _ := input["description"].(string)
	result, err := taskManager.Create(subject, desc)
	if err != nil {
		return err.Error()
	}
	return result
}

func runTaskUpdate(input map[string]any) string {
	idF, _ := input["task_id"].(float64)
	status, _ := input["status"].(string)
	var addB, remB []int
	if arr, ok := input["addBlockedBy"].([]any); ok {
		for _, v := range arr {
			if n, ok := v.(float64); ok {
				addB = append(addB, int(n))
			}
		}
	}
	if arr, ok := input["removeBlockedBy"].([]any); ok {
		for _, v := range arr {
			if n, ok := v.(float64); ok {
				remB = append(remB, int(n))
			}
		}
	}
	result, err := taskManager.Update(int(idF), tasks.Status(status), addB, remB)
	if err != nil {
		return err.Error()
	}
	return result
}

func runTaskList(input map[string]any) string {
	result, err := taskManager.List()
	if err != nil {
		return err.Error()
	}
	return result
}

func runTaskGet(input map[string]any) string {
	idF, _ := input["task_id"].(float64)
	result, err := taskManager.Get(int(idF))
	if err != nil {
		return err.Error()
	}
	return result
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
	command, _ := input["command"].(string)
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
	o := string(res)
	if o == "" {
		return "(no output)"
	}
	if len(o) > 50000 {
		return o[:50000]
	}
	return o
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
	r := strings.Join(lines, "\n")
	if len(r) > 50000 {
		r = r[:50000]
	}
	return r
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
	taskDir := filepath.Join(WorkingDir, ".tasks")
	var err error
	taskManager, err = tasks.NewTaskManager(taskDir)
	if err != nil {
		panic(err)
	}
	initHandlers()

	var messages []anthropic.MessageParam
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("s07 >> : ")
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
			if len(output) > 200 {
				fmt.Println(output[:200])
			} else {
				fmt.Println(output)
			}
			tr = append(tr, anthropic.NewToolResultBlock(b.ID, output, false))
		}
		if len(tr) == 0 {
			return nil, fmt.Errorf("stop_reason was tool_use but no tool_use blocks were returned")
		}
		*messages = append(*messages, anthropic.NewUserMessage(tr...))
	}
}
