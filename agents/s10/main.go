package main

// s10_team_protocols — Team Protocols
//
// In s09 Agent Teams, teammates communicated via inbox messages. s10 adds two FSM protocols:
//
//  1. Shutdown Protocol (Lead initiates)
//     Lead --shutdown_request(request_id)--> Teammate
//     Teammate --shutdown_response(request_id, approve)--> Lead
//     Lead tracks status via shutdownRequests[request_id].status
//
//  2. Plan Approval Protocol (Teammate initiates)
//     Teammate --plan_approval(plan text)--> Lead
//     Lead --plan_approval_response(request_id, approve)--> Teammate
//     Lead tracks status via planRequests[request_id].status

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
	"github.com/sirupsen/logrus"
	"github.com/slone/learn-claude-code-go/internal/team"
)

var MODEL string

var WorkingDir, _ = os.Getwd()
var TeamDir = filepath.Join(WorkingDir, ".team")
var InboxDir = filepath.Join(TeamDir, "inbox")

var SYSTEM_PROMPT = fmt.Sprintf(
	"You are a team lead at %s. Manage teammates with shutdown and plan approval protocols.",
	WorkingDir,
)

// Protocol trackers — protected by trackerMu because both the main goroutine
// and teammate goroutines read/write them.
type ShutdownEntry struct {
	Target string
	Status string
}

type PlanEntry struct {
	From   string
	Plan   string
	Status string
}

var shutdownRequests = map[string]*ShutdownEntry{}
var planRequests = map[string]*PlanEntry{}
var trackerMu sync.Mutex

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

var ToolSpawnTeammate = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "spawn_teammate",
		Description: anthropic.String("Spawn a persistent teammate."),
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

var ToolSendMessage = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "send_message",
		Description: anthropic.String("Send a message to a teammate."),
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
		Description: anthropic.String("Request a teammate to shut down gracefully. Returns a request_id for tracking."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"teammate": map[string]any{"type": "string"},
			},
			Required: []string{"teammate"},
		},
	},
}

var ToolLeadShutdownResponse = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "shutdown_response",
		Description: anthropic.String("Check the status of a shutdown request by request_id."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"request_id": map[string]any{"type": "string"},
			},
			Required: []string{"request_id"},
		},
	},
}

var ToolLeadPlanApproval = anthropic.ToolUnionParam{
	OfTool: &anthropic.ToolParam{
		Name:        "plan_approval",
		Description: anthropic.String("Approve or reject a teammate's plan. Provide request_id + approve + optional feedback."),
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

var LEAD_TOOLS = []anthropic.ToolUnionParam{
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
	ToolLeadPlanApproval,
}

var TEAMMATE_TOOLS = []anthropic.ToolUnionParam{
	ToolBash,
	ToolReadFile,
	ToolWriteFile,
	ToolEditFile,
	ToolSendMessage,
	ToolReadInbox,
	ToolTeammateShutdownResponse,
	ToolTeammatePlanApproval,
}

func handleShutdownRequest(teammate string) string {
	requestID := fmt.Sprintf("%x", time.Now().UnixNano())[:8]
	trackerMu.Lock()
	defer trackerMu.Unlock()

	shutdownRequests[requestID] = &ShutdownEntry{
		Target: teammate,
		Status: "pending",
	}

	payload := map[string]string{
		"message":    "Please shut down gracefully.",
		"request_id": requestID,
	}
	contentBytes, _ := json.Marshal(payload)

	bus.Send(
		"lead",
		teammate,
		string(contentBytes),
		"shutdown_request",
	)

	return fmt.Sprintf("Shutdown request %s sent to '%s' (status: pending)", requestID, teammate)
}

func checkShutdownStatus(requestID string) string {
	trackerMu.Lock()
	defer trackerMu.Unlock()

	entry := shutdownRequests[requestID]
	if entry == nil {
		return fmt.Sprintf("Shutdown request %s not found", requestID)
	}

	jsonBytes, _ := json.Marshal(entry)
	return string(jsonBytes)
}

func handlePlanReview(requestID string, approve bool, feedback string) string {
	trackerMu.Lock()
	defer trackerMu.Unlock()

	entry := planRequests[requestID]
	if entry == nil {
		return fmt.Sprintf("Plan request %s not found", requestID)
	}

	if approve {
		entry.Status = "approved"
	} else {
		entry.Status = "rejected"
	}

	payload := map[string]any{
		"request_id": requestID,
		"approve":    approve,
		"feedback":   feedback,
	}
	contentBytes, _ := json.Marshal(payload)

	bus.Send(
		"lead",
		entry.From,
		string(contentBytes),
		"plan_approval_response",
	)

	return fmt.Sprintf("Plan %s for '%s'", entry.Status, entry.From)
}

type ToolHandler func(input map[string]any) string

var bus *team.MessageBus
var teamMgr *team.TeammateManager

func buildLeadHandlers() map[string]ToolHandler {
	return map[string]ToolHandler{
		"bash":       runBash,
		"read_file":  runRead,
		"write_file": runWrite,
		"edit_file":  runEdit,
		"spawn_teammate": func(input map[string]any) string {
			name, _ := input["name"].(string)
			role, _ := input["role"].(string)
			prompt, _ := input["prompt"].(string)
			return teamMgr.Spawn(name, role, prompt)
		},
		"list_teammates": func(input map[string]any) string {
			return teamMgr.ListAll()
		},
		"send_message": func(input map[string]any) string {
			to, _ := input["to"].(string)
			content, _ := input["content"].(string)
			msgType, _ := input["msg_type"].(string)
			if msgType == "" {
				msgType = "message"
			}
			return bus.Send("lead", to, content, msgType)
		},
		"read_inbox": func(input map[string]any) string {
			msgs := bus.ReadInbox("lead")
			data, _ := json.MarshalIndent(msgs, "", "  ")
			return string(data)
		},
		"broadcast": func(input map[string]any) string {
			content, _ := input["content"].(string)
			return bus.Broadcast("lead", content, teamMgr.MemberNames())
		},
		"shutdown_request": func(input map[string]any) string {
			teammate, _ := input["teammate"].(string)
			return handleShutdownRequest(teammate)
		},
		"shutdown_response": func(input map[string]any) string {
			requestID, _ := input["request_id"].(string)
			return checkShutdownStatus(requestID)
		},
		"plan_approval": func(input map[string]any) string {
			requestID, _ := input["request_id"].(string)
			approve, _ := input["approve"].(bool)
			feedback, _ := input["feedback"].(string)
			return handlePlanReview(requestID, approve, feedback)
		},
	}
}

func buildTeammateExec() func(sender, toolName string, args map[string]any) string {
	return func(sender, toolName string, args map[string]any) string {
		switch toolName {
		case "bash":
			return runBash(args)
		case "read_file":
			return runRead(args)
		case "write_file":
			return runWrite(args)
		case "edit_file":
			return runEdit(args)
		case "shutdown_response":
			return handleShutdownResponse(sender, args)
		case "plan_approval":
			return handlePlanApproval(sender, args)
		default:
			return fmt.Sprintf("Unknown tool: %s", toolName)
		}
	}
}

func handlePlanApproval(sender string, args map[string]any) string {
	planText, _ := args["plan"].(string)
	requestID := fmt.Sprintf("%x", time.Now().UnixNano())[:8]

	trackerMu.Lock()
	defer trackerMu.Unlock()
	planRequests[requestID] = &PlanEntry{
		From:   sender,
		Plan:   planText,
		Status: "pending",
	}

	payload := map[string]any{
		"request_id": requestID,
		"plan":       planText,
	}
	contentBytes, _ := json.Marshal(payload)

	bus.Send(
		sender,
		"lead",
		string(contentBytes),
		"plan_approval_response",
	)
	return fmt.Sprintf("Plan submitted (request_id=%s). Waiting for lead approval.", requestID)
}

func handleShutdownResponse(sender string, args map[string]any) string {
	requestID := args["request_id"].(string)
	approve := args["approve"].(bool)
	reason, _ := args["reason"].(string)
	trackerMu.Lock()
	defer trackerMu.Unlock()
	if entry, ok := shutdownRequests[requestID]; ok {
		if approve {
			entry.Status = "approved"
		} else {
			entry.Status = "rejected"
		}
	}
	payload := map[string]any{
		"request_id": requestID,
		"approve":    approve,
		"reason":     reason,
	}
	contentBytes, _ := json.Marshal(payload)

	bus.Send(
		sender,
		"lead",
		string(contentBytes),
		"shutdown_response",
	)
	if approve {
		return "Shutdown approved"
	}
	return "Shutdown rejected"
}

func agentLoop(messages *[]anthropic.MessageParam, client anthropic.Client, handlers map[string]ToolHandler) (*anthropic.Message, error) {
	for {
		inbox := bus.ReadInbox("lead")
		if len(inbox) > 0 {
			data, _ := json.MarshalIndent(inbox, "", "  ")
			inboxMsg := fmt.Sprintf("<inbox>%s</inbox>", string(data))
			*messages = append(*messages, anthropic.NewUserMessage(anthropic.NewTextBlock(inboxMsg)))
		}

		resp, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
			Model:     MODEL,
			MaxTokens: 8000,
			System:    []anthropic.TextBlockParam{{Text: SYSTEM_PROMPT}},
			Tools:     LEAD_TOOLS,
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
			handler, ok := handlers[block.Name]
			var output string
			if !ok {
				output = fmt.Sprintf("Unknown tool: %s", block.Name)
			} else {
				output = handler(input)
			}
			fmt.Printf("> %s:\n", block.Name)
			if len(output) > 200 {
				fmt.Println(output[:200])
			} else {
				fmt.Println(output)
			}
			toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, output, false))
		}

		if len(toolResults) == 0 {
			return nil, fmt.Errorf("stop_reason was tool_use but no tool_use blocks found")
		}
		*messages = append(*messages, anthropic.NewUserMessage(toolResults...))
	}
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
	bus = team.NewMessageBus(InboxDir)
	teamMgr = team.NewTeammateManager(
		TeamDir, client, bus, MODEL, WorkingDir,
		TEAMMATE_TOOLS,
		buildTeammateExec(),
	)
	handlers := buildLeadHandlers()

	var messages []anthropic.MessageParam
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("\033[36ms10 >> \033[0m")
		input, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			logrus.Error(err)
			break
		}
		trimmed := strings.TrimSpace(input)

		if trimmed == "/team" {
			fmt.Println(teamMgr.ListAll())
			continue
		}
		if trimmed == "/inbox" {
			msgs := bus.ReadInbox("lead")
			data, _ := json.MarshalIndent(msgs, "", "  ")
			fmt.Println(string(data))
			continue
		}
		if isQuit(trimmed) {
			break
		}

		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(trimmed)))
		resp, loopErr := agentLoop(&messages, client, handlers)
		if loopErr != nil {
			logrus.Error(loopErr)
		} else {
			printLastReply(resp)
		}
		fmt.Println()

		if err == io.EOF {
			break
		}
	}
}
