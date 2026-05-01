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
)

var MODEL string

var WorkingDir, _ = os.Getwd()
var SYSTEM_PROMPT = fmt.Sprintf("You are a coding agent at %s. Use tools to solve tasks. Act, don't explain.", WorkingDir)

var TOOLS = []anthropic.ToolUnionParam{
	{OfTool: &anthropic.ToolParam{
		Name:        "bash",
		Description: anthropic.String("Run a shell command."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"command": map[string]any{"type": "string"},
			},
			Required: []string{"command"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name:        "read_file",
		Description: anthropic.String("Read file contents."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
			Required: []string{"path"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name:        "write_file",
		Description: anthropic.String("Write content to file."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			Required: []string{"path", "content"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
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
	}},
}

type ToolHandler func(input map[string]any) string

var TOOL_HANDLERS = map[string]ToolHandler{
	"bash":       runBash,
	"read_file":  runRead,
	"write_file": runWrite,
	"edit_file":  runEdit,
}

func isQuit(s string) bool {
	lower := strings.ToLower(s)
	return lower == "exit" || lower == "q" || lower == "quit" || s == ""
}

func printLastReply(resp *anthropic.Message) {
	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			fmt.Println(block.Text)
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
		fmt.Print("s02 >> : ")
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

func runBash(input map[string]any) string {
	command, ok := input["command"].(string)
	if !ok {
		return "Error: command is not a string"
	}
	fmt.Printf("\033[33m$ %s\033[0m\n", command)

	dangerous := []string{"rm -rf /", "sudo", "shutdown", "reboot", "> /dev/"}
	for _, str := range dangerous {
		if strings.Contains(command, str) {
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
	switch {
	case output == "":
		return "(no output)"
	case len(output) > 50000:
		return output[:50000]
	default:
		return output
	}
}

func runRead(input map[string]any) string {
	path, ok := input["path"].(string)
	if !ok {
		return "Error: path is not a string"
	}
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> read_file: %s\033[0m\n", path)

	limit := 0
	if limitVal, ok := input["limit"]; ok {
		if f, ok := limitVal.(float64); ok {
			limit = int(f)
		}
	}

	content, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
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
	return result
}

func runWrite(input map[string]any) string {
	path, ok := input["path"].(string)
	if !ok {
		return "Error: path is not a string"
	}
	content, ok := input["content"].(string)
	if !ok {
		return "Error: content is not a string"
	}
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> write_file: %s\033[0m\n", path)

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
}

func runEdit(input map[string]any) string {
	path, ok := input["path"].(string)
	if !ok {
		return "Error: path is not a string"
	}
	oldText, ok := input["old_text"].(string)
	if !ok {
		return "Error: old_text is not a string"
	}
	newText, ok := input["new_text"].(string)
	if !ok {
		return "Error: new_text is not a string"
	}
	safePath, err := getSafePath(path)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("\033[33m> edit_file: %s\033[0m\n", path)

	content, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if !strings.Contains(string(content), oldText) {
		return fmt.Sprintf("Error: Text not found in %s", path)
	}
	newContent := strings.Replace(string(content), oldText, newText, 1)
	if err := os.WriteFile(safePath, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return fmt.Sprintf("Edited %s", path)
}

func agentLoop(messages *[]anthropic.MessageParam, client anthropic.Client) (*anthropic.Message, error) {
	for {
		resp, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
			Model:     MODEL,
			MaxTokens: 8000,
			System:    []anthropic.TextBlockParam{{Text: SYSTEM_PROMPT}},
			Tools:     TOOLS,
			Messages:  *messages,
		})
		if err != nil {
			return nil, err
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
		*messages = append(*messages, anthropic.NewAssistantMessage(assistantBlocks...))

		if resp.StopReason != anthropic.StopReasonToolUse {
			return resp, nil
		}

		var toolResults []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}

			var input map[string]any
			if err := json.Unmarshal(block.Input, &input); err != nil {
				return nil, fmt.Errorf("decode tool input for %s: %w", block.Name, err)
			}

			handler, ok := TOOL_HANDLERS[block.Name]
			var output string
			if !ok {
				output = fmt.Sprintf("Unknown tool: %s", block.Name)
			} else {
				output = handler(input)
			}

			if len(output) > 200 {
				fmt.Println(output[:200])
			} else {
				fmt.Println(output)
			}

			toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, output, false))
		}

		if len(toolResults) == 0 {
			return nil, fmt.Errorf("stop_reason was tool_use but no tool_use blocks were returned")
		}

		*messages = append(*messages, anthropic.NewUserMessage(toolResults...))
	}
}
