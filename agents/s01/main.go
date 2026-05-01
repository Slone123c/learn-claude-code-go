package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
)

var MODEL string

var WorkingDir, _ = os.Getwd()
var SYSTEM_PROMPT = fmt.Sprintf("You are a coding agent at %s. Use bash to solve tasks. Act, don't explain.", WorkingDir)

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
		fmt.Print("s01 >> : ")
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

func runBash(command string) string {
	dangerous := []string{"rm -rf /", "sudo", "shutdown", "reboot", "> /dev/"}
	for _, str := range dangerous {
		if strings.Contains(command, str) {
			return "Error: Dangerous command blocked"
		}
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = WorkingDir

	res, err := cmd.CombinedOutput()
	if err != nil {
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

			command, ok := input["command"].(string)
			if !ok {
				return nil, fmt.Errorf("tool %s missing string command", block.Name)
			}

			output := runBash(command)
			fmt.Printf("\033[33m$ %s\033[0m\n", command)
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
