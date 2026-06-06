package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino-ext/components/model/openai"
	mcptool "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	os.Exit(run())
}

func run() int {
	loadEnv()

	ctx, cancel := context.WithTimeout(context.Background(), durationEnv("DEMO_AGENT_TIMEOUT", 5*time.Minute))
	defer cancel()

	if err := ensureEnv(); err != nil {
		log.Print(err)
		return 1
	}

	fmt.Printf("Starting supply-chain detective client with model %q.\n", os.Getenv("OPENAI_MODEL"))
	fmt.Printf("Connecting to mssql-mcp in %q for database %q.\n", serverDir(), os.Getenv("MSSQL_DATABASE"))

	mcpClient, err := connectMCP(ctx)
	if err != nil {
		log.Printf("connect to mssql-mcp: %v", err)
		return 1
	}
	defer func() {
		if err := mcpClient.Close(); err != nil {
			log.Printf("close MCP client: %v", err)
		}
	}()

	answer, err := runAgent(ctx, mcpClient)
	if err != nil {
		log.Printf("run Eino agent: %v", err)
		return 1
	}

	fmt.Println()
	fmt.Println("=== Final Answer ===")
	fmt.Println(answer)
	return 0
}

func loadEnv() {
	for _, path := range []string{".env", "../.env", "../../.env"} {
		_ = godotenv.Load(path)
	}
}

func ensureEnv() error {
	required := []string{
		"OPENAI_API_KEY",
		"MSSQL_SERVER",
		"MSSQL_PORT",
		"MSSQL_DATABASE",
		"MSSQL_USERNAME",
		"MSSQL_PASSWORD",
	}
	var missing []string
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	defaultEnv("MSSQL_TRUST_SERVER_CERTIFICATE", "true")
	defaultEnv("MSSQL_ACCESS_LEVEL", "READONLY")
	defaultEnv("OPENAI_MODEL", "gpt-4o-mini")
	return nil
}

func connectMCP(ctx context.Context) (*client.Client, error) {
	stdio := transport.NewStdioWithOptions(
		"go",
		os.Environ(),
		[]string{"run", "./cmd/mssql-mcp"},
		transport.WithCommandFunc(func(ctx context.Context, _ string, env []string, _ []string) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, "go", "run", "./cmd/mssql-mcp")
			cmd.Dir = serverDir()
			cmd.Env = append(os.Environ(), env...)
			return cmd, nil
		}),
	)

	c := client.NewClient(stdio)
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start stdio MCP client: %w", err)
	}
	if stderr, ok := client.GetStderr(c); ok {
		go func() {
			if _, err := io.Copy(os.Stderr, stderr); err != nil && !errors.Is(err, io.EOF) {
				log.Printf("read mssql-mcp stderr: %v", err)
			}
		}()
	}

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "supply-chain-detective-eino-client",
		Version: "0.1.0",
	}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}

	if _, err := c.Initialize(ctx, initRequest); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP session: %w", err)
	}
	return c, nil
}

func runAgent(ctx context.Context, mcpClient *client.Client) (string, error) {
	tools, err := mcptool.GetTools(ctx, &mcptool.Config{
		Cli:                   mcpClient,
		ToolCallResultHandler: toolCallResultHandler,
	})
	if err != nil {
		return "", fmt.Errorf("load MCP tools for Eino: %w", err)
	}
	if len(tools) == 0 {
		return "", fmt.Errorf("mssql-mcp exposed no tools")
	}
	if err := printTools(ctx, tools); err != nil {
		return "", err
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:     os.Getenv("OPENAI_API_KEY"),
		Model:      os.Getenv("OPENAI_MODEL"),
		BaseURL:    os.Getenv("OPENAI_BASE_URL"),
		ByAzure:    strings.EqualFold(os.Getenv("OPENAI_BY_AZURE"), "true"),
		APIVersion: os.Getenv("OPENAI_API_VERSION"),
		Timeout:    durationEnv("OPENAI_TIMEOUT", 2*time.Minute),
	})
	if err != nil {
		return "", fmt.Errorf("create OpenAI chat model: %w", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "supply-chain-detective",
		Description: "Analyzes the Northwind Relay SQL Server demo through mssql-mcp tools.",
		Instruction: agentInstruction(),
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
		MaxIterations: intEnv("DEMO_AGENT_MAX_ITERATIONS", 24),
	})
	if err != nil {
		return "", fmt.Errorf("create Eino chat model agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: false})
	prompt := investigationPrompt()
	printPrompt(prompt)
	iter := runner.Query(ctx, prompt)

	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		message, ok, err := eventMessage(event)
		if err != nil {
			return "", err
		}
		if ok && message.Role == schema.Assistant && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
		if ok {
			printMessageEvent(event, message)
		} else {
			printAgentEvent(event)
		}
	}
	if strings.TrimSpace(final) == "" {
		return "", fmt.Errorf("agent finished without a final answer")
	}
	return final, nil
}

func eventMessage(event *adk.AgentEvent) (*schema.Message, bool, error) {
	if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return nil, false, nil
	}
	message, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		return nil, false, err
	}
	return message, message != nil, nil
}

func printTools(ctx context.Context, tools []tool.BaseTool) error {
	fmt.Println()
	fmt.Println("=== MCP Tools ===")
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return fmt.Errorf("read MCP tool info: %w", err)
		}
		fmt.Printf("- %s: %s\n", info.Name, strings.TrimSpace(info.Desc))
	}
	return nil
}

func printPrompt(prompt string) {
	fmt.Println()
	fmt.Println("=== User Prompt ===")
	fmt.Println(prompt)
	fmt.Println()
	fmt.Println("=== Conversation Trace ===")
}

func printMessageEvent(event *adk.AgentEvent, message *schema.Message) {
	label := string(message.Role)
	if event != nil && event.AgentName != "" {
		label = fmt.Sprintf("%s / %s", event.AgentName, label)
	}
	if message.Role == schema.Tool && message.ToolName != "" {
		label = fmt.Sprintf("%s / %s", label, message.ToolName)
	}

	fmt.Println()
	fmt.Printf("--- %s ---\n", label)

	if strings.TrimSpace(message.Content) != "" {
		fmt.Println(message.Content)
	}
	for _, call := range message.ToolCalls {
		fmt.Printf("tool_call id=%s name=%s\n", call.ID, call.Function.Name)
		if strings.TrimSpace(call.Function.Arguments) != "" {
			fmt.Println(prettyJSON(call.Function.Arguments))
		}
	}
	if message.Role == schema.Tool && message.ToolCallID != "" {
		fmt.Printf("tool_call_id=%s\n", message.ToolCallID)
	}
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		fmt.Printf("tokens prompt=%d completion=%d total=%d\n", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
}

func printAgentEvent(event *adk.AgentEvent) {
	if event == nil || event.Action == nil {
		return
	}
	fmt.Println()
	fmt.Printf("--- %s / action ---\n", event.AgentName)
	raw, err := sonic.MarshalString(event.Action)
	if err != nil {
		fmt.Printf("%v\n", event.Action)
		return
	}
	fmt.Println(prettyJSON(raw))
}

func prettyJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return out.String()
}

func toolCallResultHandler(_ context.Context, name string, result *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	if result == nil || !result.IsError {
		return result, nil
	}
	raw, err := sonic.MarshalString(result)
	if err != nil {
		raw = fmt.Sprintf("%v", result)
	}
	return mcp.NewToolResultText(fmt.Sprintf("Tool %q returned an error. Treat this as an observation and try a corrected SQL Server query or a different schema-inspection tool. Error: %s", name, raw)), nil
}

func serverDir() string {
	if dir := strings.TrimSpace(os.Getenv("MSSQL_MCP_SERVER_DIR")); dir != "" {
		return dir
	}
	return "../.."
}

func defaultEnv(name, value string) {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		_ = os.Setenv(name, value)
	}
}

func intEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err == nil && value > 0 {
		return value
	}
	var seconds int
	if _, err := fmt.Sscanf(raw, "%d", &seconds); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func agentInstruction() string {
	return `You are an operations detective for Northwind Relay.

You must answer only from evidence you obtain through the mssql-mcp tools. Start with schema discovery, then inspect tables, relationships, profiles, and data. Prefer read-only SQL through read_data after you understand the schema.

Rules:
- Use the database tools. Do not invent facts.
- The SQL dialect is Microsoft SQL Server T-SQL. Avoid reserved identifiers as aliases, including current_user; use names such as customer_name or app_user instead.
- If a tool returns an SQL syntax or schema error, inspect the schema and retry with corrected T-SQL.
- Include concise SQL evidence or tool evidence behind each finding.
- Keep the final report structured and executive-friendly.
- Answer every question the user asks, then give the next three recommended actions.`
}

func investigationPrompt() string {
	return `Analyze the Northwind Relay supply-chain database and answer all of these questions:

1. Which supplier is causing operational risk?
2. Which products are near stockout despite strong demand?
3. Are delayed shipments connected to quality incidents?
4. Are there customers or payments that deserve extra scrutiny?
5. Which tables and relationships matter for the analysis?

Use the mssql-mcp tools such as list_table, search_schema, describe_table, inspect_relationships, profile_table, and read_data. Include the SQL queries or tool calls that support each finding. End with the next three actions.`
}
