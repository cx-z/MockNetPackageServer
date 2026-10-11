package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/getmockd/mockd/pkg/cli/internal/flags"
	"github.com/getmockd/mockd/pkg/cli/internal/output"
	"github.com/getmockd/mockd/pkg/cli/internal/parse"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/mock"
	"github.com/spf13/cobra"
)

var (
	addMockType       string
	addName           string
	addAllowDuplicate bool
	addStateful       string

	addMethod       string
	addPath         string
	addStatus       int
	addBody         string
	addBodyFile     string
	addHeaders      flags.StringSlice
	addMatchHeaders flags.StringSlice
	addMatchQueries flags.StringSlice
	addBodyContains string
	addPathPattern  string
	addPriority     int
	addDelay        int

	addSSE          bool
	addSSEEvents    flags.StringSlice
	addSSEDelay     int
	addSSETemplate  string
	addSSERepeat    int
	addSSEKeepalive int

	addMessage string
	addEcho    bool

	addOperation string
	addResponse  string

	addService    string
	addRPCMethod  string
	addGRPCPort   int
	addProtoFiles flags.StringSlice
	addProtoPaths flags.StringSlice

	addActionAlias       string
	addStatefulOperation string

	addTable      string
	addBindAction string
)

var addCmd = &cobra.Command{
	Use:   "add [type]",
	Short: "Add a new mock endpoint",
	Long: `Add a new mock endpoint.

The mock type can be specified as a positional argument, a flag, or via
the protocol subcommands:

  mockd add http --path /api/hello          # positional type
  mockd add --type http --path /api/hello   # flag type
  mockd http add --path /api/hello          # subcommand

Valid types: http, websocket, grpc`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAdd,
}

func init() {
	rootCmd.AddCommand(addCmd)

	addCmd.Flags().StringVarP(&addMockType, "type", "t", "http", "Mock type (http, websocket, grpc)")
	addCmd.Flags().StringVarP(&addName, "name", "n", "", "Mock display name")
	addCmd.Flags().BoolVar(&addAllowDuplicate, "allow-duplicate", false, "Create a new mock even if one already exists on the same path")
	addCmd.Flags().StringVar(&addStateful, "stateful", "", "Create a stateful CRUD resource (value is the resource name, e.g., users)")

	addCmd.Flags().StringVarP(&addMethod, "method", "m", "GET", "HTTP method to match")
	addCmd.Flags().StringVar(&addPath, "path", "", "URL path to match (required for http, websocket)")
	addCmd.Flags().IntVarP(&addStatus, "status", "s", 200, "Response status code")
	addCmd.Flags().StringVarP(&addBody, "body", "b", "", "Response body")
	addCmd.Flags().StringVar(&addBodyFile, "body-file", "", "Read response body from file")
	addCmd.Flags().VarP(&addHeaders, "header", "H", "Response header (key:value), repeatable")
	addCmd.Flags().Var(&addMatchHeaders, "match-header", "Required request header (key:value), repeatable")
	addCmd.Flags().Var(&addMatchQueries, "match-query", "Required query param (key=value or key:value), repeatable")
	addCmd.Flags().StringVar(&addBodyContains, "match-body-contains", "", "Match requests whose body contains this string")
	addCmd.Flags().StringVar(&addPathPattern, "path-pattern", "", "Regex path pattern for matching (alternative to --path)")
	addCmd.Flags().IntVar(&addPriority, "priority", 0, "Mock priority (higher = matched first)")
	addCmd.Flags().IntVar(&addDelay, "delay", 0, "Response delay in milliseconds")

	addCmd.Flags().BoolVar(&addSSE, "sse", false, "Enable Server-Sent Events streaming")
	addCmd.Flags().Var(&addSSEEvents, "sse-event", "SSE event (type:data or just data), repeatable")
	addCmd.Flags().IntVar(&addSSEDelay, "sse-delay", 100, "Delay between SSE events in milliseconds")
	addCmd.Flags().StringVar(&addSSETemplate, "sse-template", "", "SSE template: openai, notification")
	addCmd.Flags().IntVar(&addSSERepeat, "sse-repeat", 1, "Number of times to repeat SSE events (0 = infinite)")
	addCmd.Flags().IntVar(&addSSEKeepalive, "sse-keepalive", 0, "SSE keepalive interval in seconds (0 = disabled, min 5)")

	addCmd.Flags().StringVar(&addMessage, "message", "", "Default response message (JSON) for WebSocket")
	addCmd.Flags().BoolVar(&addEcho, "echo", false, "Enable echo mode for WebSocket")

	addCmd.Flags().StringVar(&addResponse, "response", "", "JSON response data (for grpc)")

	addCmd.Flags().StringVar(&addService, "service", "", "gRPC service name (e.g., greeter.Greeter)")
	addCmd.Flags().StringVar(&addRPCMethod, "rpc-method", "", "gRPC method name")
	addCmd.Flags().IntVar(&addGRPCPort, "grpc-port", 50051, "gRPC server port")
	addCmd.Flags().Var(&addProtoFiles, "proto", "Path to .proto file (required for gRPC, repeatable)")
	addCmd.Flags().Var(&addProtoPaths, "proto-path", "Import path for proto dependencies (repeatable)")

	addCmd.Flags().StringVar(&addStatefulOperation, "stateful-operation", "", "Wire to a custom stateful operation (e.g., TransferFunds)")

	addCmd.Flags().StringVar(&addTable, "table", "", "Bind to a stateful resource table (e.g., users)")
	addCmd.Flags().StringVar(&addBindAction, "bind", "", "Stateful action: list, get, create, update, patch, delete, custom")

}

func runAdd(cmd *cobra.Command, args []string) error { //nolint:gocyclo // CLI dispatch function
	// Accept positional type: "mockd add http --path ..."
	// Only override if --type wasn't explicitly set by the user
	if len(args) == 1 && !cmd.Flags().Changed("type") {
		addMockType = args[0]
	}

	// Resolve flag aliases:
	if addActionAlias != "" && addOperation == "" {
		addOperation = addActionAlias
	}

	// Normalize mock type
	mt := strings.ToLower(addMockType)

	// Validate mock type
	validTypes := map[string]mock.Type{
		"http":      mock.TypeHTTP,
		"websocket": mock.TypeWebSocket,
		"grpc":      mock.TypeGRPC,
	}

	mockTypeEnum, ok := validTypes[mt]
	if !ok {
		return fmt.Errorf("invalid mock type: %s\n\nValid types: http, websocket, grpc", addMockType)
	}

	// Handle --stateful: create a stateful CRUD resource instead of a mock
	if addStateful != "" {
		return runAddStateful(addStateful)
	}

	// Mutual exclusivity: --path and --path-pattern
	if addPath != "" && addPathPattern != "" {
		return errors.New("--path and --path-pattern are mutually exclusive")
	}

	// --table/--bind is only supported for protocols with stateful bindings
	if addTable != "" && mockTypeEnum != mock.TypeHTTP {
		return fmt.Errorf("--table/--bind is supported for http mocks, not %s", mt)
	}

	// Build mock configuration based on type
	var m *config.MockConfiguration
	var err error

	switch mockTypeEnum {
	case mock.TypeHTTP:
		m, err = buildHTTPMock(addName, addPath, addMethod, addStatus, addBody, addBodyFile, addPriority, addDelay, addHeaders, addMatchHeaders, addMatchQueries,
			addSSE, addSSEEvents, addSSEDelay, addSSETemplate, addSSERepeat, addSSEKeepalive, addBodyContains, addPathPattern, addStatefulOperation, addTable, addBindAction, addOperation)
	case mock.TypeWebSocket:
		m, err = buildWebSocketMock(addName, addPath, addMessage, addEcho)
	case mock.TypeGRPC:
		m, err = buildGRPCMock(addName, addService, addRPCMethod, addResponse, addGRPCPort, addProtoFiles, addProtoPaths)
	}

	if err != nil {
		return err
	}

	// Create admin client
	client := NewAdminClientWithAuth(adminURL)

	// For HTTP mocks: check for existing mock on same method+path (upsert behavior)
	if mockTypeEnum == mock.TypeHTTP && !addAllowDuplicate && m.HTTP != nil && m.HTTP.Matcher != nil {
		existingID, err := findExistingHTTPMock(client, m.HTTP.Matcher.Method, m.HTTP.Matcher.Path)
		if err != nil {
			// Connection error — fall through to create which will give a better error
			return fmt.Errorf("%s", FormatConnectionError(err))
		}
		if existingID != "" {
			// Update existing mock instead of creating a duplicate
			updated, err := client.UpdateMock(existingID, m)
			if err != nil {
				return fmt.Errorf("failed to update mock: %s", FormatConnectionError(err))
			}
			return outputResult(&CreateMockResult{
				Mock:   updated,
				Action: "updated",
			}, mockTypeEnum, jsonOutput)
		}
	}

	// Create new mock
	result, err := client.CreateMock(resolvedWorkspace(), m)
	if err != nil {
		return fmt.Errorf("%s", FormatConnectionError(err))
	}

	// Output result based on mock type and action (created vs merged)
	return outputResult(result, mockTypeEnum, jsonOutput)
}

// findExistingHTTPMock looks for an existing mock with the same method+path.
// Returns the mock ID if found, empty string if not.
func findExistingHTTPMock(client AdminClient, method, path string) (string, error) {
	mocks, err := client.ListMocks(resolvedWorkspace())
	if err != nil {
		return "", err
	}

	for _, m := range mocks {
		if m.Type != mock.TypeHTTP && m.Type != "" {
			continue
		}
		if m.HTTP == nil || m.HTTP.Matcher == nil {
			continue
		}
		if m.HTTP.Matcher.Method == method && m.HTTP.Matcher.Path == path {
			return m.ID, nil
		}
	}
	return "", nil
}

// buildHTTPMock creates an HTTP mock configuration.
//
//nolint:gocyclo // CLI flag builder with many independent branches — not meaningfully decomposable.
func buildHTTPMock(name, path, method string, status int, body, bodyFile string, priority, delay int,
	headers, matchHeaders, matchQueries flags.StringSlice,
	sse bool, sseEvents flags.StringSlice, sseDelay int, sseTemplate string, sseRepeat, sseKeepalive int,
	bodyContains, pathPattern, statefulOperation, table, bindAction, operation string) (*config.MockConfiguration, error) {

	if path == "" && pathPattern == "" {
		return nil, errors.New(`--path or --path-pattern is required for HTTP mocks

Usage: mockd add --path /api/endpoint [flags]

Run 'mockd add --help' for more options`)
	}

	// Validate --table/--bind: both or neither
	if (table != "") != (bindAction != "") {
		return nil, errors.New("--table and --bind must be used together\n\nUsage: mockd add http --path /api/users --table users --bind list")
	}

	// Validate bind action values
	if bindAction != "" {
		validBindActions := map[string]bool{
			"list": true, "get": true, "create": true,
			"update": true, "patch": true, "delete": true, "custom": true,
		}
		if !validBindActions[bindAction] {
			return nil, fmt.Errorf("invalid --bind value %q: must be one of list, get, create, update, patch, delete, custom", bindAction)
		}
		// --table/--bind is mutually exclusive with --body, --body-file, --sse, --stateful-operation
		if body != "" || bodyFile != "" {
			return nil, errors.New("--table/--bind and --body/--body-file are mutually exclusive (the table provides the response)")
		}
		if sse || len(sseEvents) > 0 || sseTemplate != "" {
			return nil, errors.New("--table/--bind and --sse are mutually exclusive")
		}
		if statefulOperation != "" {
			return nil, errors.New("--table/--bind and --stateful-operation are mutually exclusive")
		}
	}

	// Validate stateful-operation incompatibilities before touching files.
	// This ensures users get the real flag error rather than a body-file I/O error.
	if statefulOperation != "" {
		if sse || len(sseEvents) > 0 || sseTemplate != "" {
			return nil, errors.New("--stateful-operation and --sse are mutually exclusive")
		}
		if body != "" || bodyFile != "" {
			return nil, errors.New("--stateful-operation and --body/--body-file are mutually exclusive (the operation result becomes the response)")
		}
	}

	// Read body from file if specified
	responseBody := body
	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read body file: %w", err)
		}
		responseBody = string(data)
	}

	// Parse response headers
	responseHeaders := make(map[string]string)
	for _, h := range headers {
		key, value, ok := parse.KeyValue(h, ':')
		if !ok {
			return nil, fmt.Errorf("invalid header format: %s (expected key:value)", h)
		}
		responseHeaders[key] = value
	}

	// Parse match headers
	matchHeadersMap := make(map[string]string)
	for _, h := range matchHeaders {
		key, value, ok := parse.KeyValue(h, ':')
		if !ok {
			return nil, fmt.Errorf("invalid match-header format: %s (expected key:value)", h)
		}
		matchHeadersMap[key] = value
	}

	// Parse match query params — accept both key=value and key:value
	matchQueryMap := make(map[string]string)
	for _, q := range matchQueries {
		key, value, ok := parse.KeyValue(q, '=', ':')
		if !ok {
			return nil, fmt.Errorf("invalid match-query format: %s (expected key=value or key:value)", q)
		}
		matchQueryMap[key] = value
	}

	enabled := true
	m := &config.MockConfiguration{
		Name:    name,
		Type:    mock.TypeHTTP,
		Enabled: &enabled,
		HTTP: &mock.HTTPSpec{
			Priority: priority,
			Matcher: &mock.HTTPMatcher{
				Method: strings.ToUpper(method),
				Path:   path,
			},
		},
	}

	// Handle stateful operation: mutually exclusive with response/sse
	switch {
	case table != "":
		binding := &mock.StatefulBinding{
			Table:  table,
			Action: bindAction,
		}
		if bindAction == "custom" {
			if operation == "" {
				return nil, errors.New("--operation is required when using --bind custom\n\nUsage: mockd add http --path /api/users/{id}/verify --table users --bind custom --operation VerifyUser")
			}
			binding.Operation = operation
		}
		m.HTTP.StatefulBinding = binding
	case statefulOperation != "":
		m.HTTP.StatefulOperation = statefulOperation
	case sse || len(sseEvents) > 0 || sseTemplate != "":
		// Build SSE config if enabled
		sseConfig, err := buildSSEConfig(sseEvents, sseDelay, sseTemplate, sseRepeat, sseKeepalive)
		if err != nil {
			return nil, err
		}
		m.HTTP.SSE = sseConfig
	default:
		// Only set Response for non-SSE, non-stateful-operation mocks
		m.HTTP.Response = &mock.HTTPResponse{
			StatusCode: status,
			Body:       responseBody,
			DelayMs:    delay,
		}
		if len(responseHeaders) > 0 {
			m.HTTP.Response.Headers = responseHeaders
		}
	}

	if len(matchHeadersMap) > 0 {
		m.HTTP.Matcher.Headers = matchHeadersMap
	}
	if len(matchQueryMap) > 0 {
		m.HTTP.Matcher.QueryParams = matchQueryMap
	}
	if bodyContains != "" {
		m.HTTP.Matcher.BodyContains = bodyContains
	}
	if pathPattern != "" {
		m.HTTP.Matcher.PathPattern = pathPattern
	}

	return m, nil
}

// buildSSEConfig creates an SSE configuration from CLI flags.
//
//nolint:unparam // error is always nil but kept for future validation
func buildSSEConfig(events flags.StringSlice, delayMs int, template string, repeat, keepaliveMs int) (*mock.SSEConfig, error) {
	cfg := &mock.SSEConfig{
		Timing: mock.SSETimingConfig{
			FixedDelay: &delayMs,
		},
		Lifecycle: mock.SSELifecycleConfig{
			Termination: mock.SSETerminationConfig{
				Type: "complete",
			},
		},
	}

	// Set keepalive if specified
	if keepaliveMs > 0 {
		cfg.Lifecycle.KeepaliveInterval = keepaliveMs
	}

	// Handle template-based generation
	if template != "" {
		cfg.Template = template
		return cfg, nil
	}

	// Parse events from CLI
	if len(events) == 0 {
		// Default to a simple "connected" event
		cfg.Events = []mock.SSEEventDef{
			{Type: "message", Data: map[string]interface{}{"status": "connected"}},
		}
		return cfg, nil
	}

	parsedEvents := make([]mock.SSEEventDef, 0, len(events))
	for _, e := range events {
		eventType, data, hasType := parse.KeyValue(e, ':')
		if !hasType {
			// No type specified, use "message" as default
			parsedEvents = append(parsedEvents, mock.SSEEventDef{
				Type: "message",
				Data: e,
			})
		} else {
			// Try to parse data as JSON
			var jsonData interface{}
			if err := json.Unmarshal([]byte(data), &jsonData); err != nil {
				// Not JSON, use as string
				parsedEvents = append(parsedEvents, mock.SSEEventDef{
					Type: eventType,
					Data: data,
				})
			} else {
				parsedEvents = append(parsedEvents, mock.SSEEventDef{
					Type: eventType,
					Data: jsonData,
				})
			}
		}
	}
	cfg.Events = parsedEvents

	// Handle repeat (infinite streaming)
	if repeat == 0 {
		// Infinite repeat - use generator with template
		cfg.Generator = &mock.SSEEventGenerator{
			Type: "template",
			Template: &mock.SSETemplateGenerator{
				Events: parsedEvents,
				Repeat: 0, // 0 means infinite in template generator
			},
		}
		cfg.Events = nil // Clear events since we're using generator
	} else if repeat > 1 {
		// Repeat N times using generator
		cfg.Generator = &mock.SSEEventGenerator{
			Type: "template",
			Template: &mock.SSETemplateGenerator{
				Events: parsedEvents,
				Repeat: repeat,
			},
		}
		cfg.Events = nil
	}

	return cfg, nil
}

// buildWebSocketMock creates a WebSocket mock configuration.
func buildWebSocketMock(name, path, message string, echo bool) (*config.MockConfiguration, error) {
	if path == "" {
		return nil, errors.New(`--path is required for WebSocket mocks

Usage: mockd add --type websocket --path /ws/endpoint [flags]

Run 'mockd add --help' for more options`)
	}

	wsEnabled := true
	m := &config.MockConfiguration{
		Name:    name,
		Type:    mock.TypeWebSocket,
		Enabled: &wsEnabled,
		WebSocket: &mock.WebSocketSpec{
			Path: path,
		},
	}

	// Set echo mode
	if echo {
		m.WebSocket.EchoMode = &echo
	}

	// Set default response message
	if message != "" {
		m.WebSocket.DefaultResponse = &mock.WSMessageResponse{
			Type:  "json",
			Value: json.RawMessage(message),
		}
	}

	return m, nil
}

// buildGRPCMock creates a gRPC mock configuration.
func buildGRPCMock(name, service, rpcMethod, response string, port int, protoFiles, protoPaths flags.StringSlice) (*config.MockConfiguration, error) {
	// Proto file is required for gRPC mocks
	if len(protoFiles) == 0 {
		return nil, errors.New(`--proto is required for gRPC mocks

gRPC mocks require a .proto file to define the service schema.

Usage: mockd add --type grpc --proto ./service.proto --service myapp.UserService --rpc-method GetUser [flags]

Examples:
  # Basic gRPC mock
  mockd add --type grpc \
    --proto ./service.proto \
    --service myapp.UserService \
    --rpc-method GetUser \
    --response '{"id": "1", "name": "Alice"}'

  # With import paths for proto dependencies
  mockd add --type grpc \
    --proto ./api/v1/service.proto \
    --proto-path ./api \
    --service myapp.v1.UserService \
    --rpc-method GetUser \
    --response '{"id": "1"}'

Run 'mockd add --help' for more options`)
	}

	if service == "" {
		return nil, errors.New(`--service is required for gRPC mocks

Usage: mockd add --type grpc --proto ./service.proto --service myapp.UserService --rpc-method GetUser [flags]

Run 'mockd add --help' for more options`)
	}

	if rpcMethod == "" {
		return nil, errors.New(`--rpc-method is required for gRPC mocks

Usage: mockd add --type grpc --proto ./service.proto --service myapp.UserService --rpc-method GetUser [flags]

Run 'mockd add --help' for more options`)
	}

	// Verify proto files exist
	for _, protoFile := range protoFiles {
		if _, err := os.Stat(protoFile); os.IsNotExist(err) {
			return nil, fmt.Errorf("proto file not found: %s", protoFile)
		}
	}

	grpcEnabled := true
	m := &config.MockConfiguration{
		Name:    name,
		Type:    mock.TypeGRPC,
		Enabled: &grpcEnabled,
		GRPC: &mock.GRPCSpec{
			Port:       port,
			Reflection: true, // Enable reflection for grpcurl/grpcui support
			Services:   make(map[string]mock.ServiceConfig),
		},
	}

	// Set proto file(s)
	if len(protoFiles) == 1 {
		m.GRPC.ProtoFile = protoFiles[0]
	} else {
		m.GRPC.ProtoFiles = protoFiles
	}

	// Set import paths if provided
	if len(protoPaths) > 0 {
		m.GRPC.ImportPaths = protoPaths
	}

	methodConfig := mock.MethodConfig{}

	// Parse response if provided
	if response != "" {
		var responseData interface{}
		if err := json.Unmarshal([]byte(response), &responseData); err != nil {
			return nil, fmt.Errorf("invalid JSON response: %w", err)
		}
		methodConfig.Response = responseData
	}

	m.GRPC.Services[service] = mock.ServiceConfig{
		Methods: map[string]mock.MethodConfig{
			rpcMethod: methodConfig,
		},
	}

	return m, nil
}

// When --path is provided, it generates 5 CRUD mocks bound to the table.
// Without --path, it creates a bridge-only table (no HTTP endpoints).
func runAddStateful(name string) error {
	collection := &config.MockCollection{
		Version: "1.0",
		Mocks:   []*config.MockConfiguration{},
		StatefulResources: []*config.StatefulResourceConfig{
			{
				Name: name,
			},
		},
	}

	// If --path is provided, generate 5 CRUD mocks with statefulBinding
	if addPath != "" {
		basePath := strings.TrimSuffix(addPath, "/")
		itemPath := basePath + "/{id}"

		crudOps := []struct {
			method string
			path   string
			action string
		}{
			{"GET", basePath, "list"},
			{"POST", basePath, "create"},
			{"GET", itemPath, "get"},
			{"PUT", itemPath, "update"},
			{"DELETE", itemPath, "delete"},
		}

		enabled := true
		for _, op := range crudOps {
			m := &mock.Mock{
				Type:    mock.TypeHTTP,
				Enabled: &enabled,
				HTTP: &mock.HTTPSpec{
					Matcher: &mock.HTTPMatcher{
						Method: op.method,
						Path:   op.path,
					},
					StatefulBinding: &mock.StatefulBinding{
						Table:  name,
						Action: op.action,
					},
				},
			}
			collection.Mocks = append(collection.Mocks, m)
		}
	}

	client := NewAdminClientWithAuth(adminURL)
	_, err := client.ImportConfig(collection, false, resolvedWorkspace())
	if err != nil {
		return fmt.Errorf("%s", FormatConnectionError(err))
	}

	if jsonOutput {
		return output.JSON(struct {
			Resource  string `json:"resource"`
			Action    string `json:"action"`
			Endpoints int    `json:"endpoints,omitempty"`
			BasePath  string `json:"basePath,omitempty"`
		}{
			Resource:  name,
			Action:    "created",
			Endpoints: len(collection.Mocks),
			BasePath:  addPath,
		})
	}

	fmt.Printf("Created stateful resource: %s\n", name)
	if addPath != "" {
		fmt.Printf("  Endpoints:\n")
		for _, m := range collection.Mocks {
			fmt.Printf("    %s %s → %s\n", m.HTTP.Matcher.Method, m.HTTP.Matcher.Path, m.HTTP.StatefulBinding.Action)
		}
	} else {
		fmt.Printf("  Access via: extend bindings or protocol integrations\n")
	}
	return nil
}

// outputResult formats and prints the created or merged mock result.
func outputResult(result *CreateMockResult, mockType mock.Type, jsonOutput bool) error { //nolint:gocyclo // CLI output handler for all mock types
	if jsonOutput {
		return outputJSONResult(result, mockType)
	}

	created := result.Mock

	// Handle merge case
	if result.IsMerge() {
		fmt.Printf("Merged into mock: %s\n", created.ID)
		fmt.Printf("  Type: %s\n", created.Type)

		switch mockType { //nolint:exhaustive // only gRPC has merge output
		case mock.TypeGRPC:
			if len(result.AddedServices) > 0 {
				fmt.Printf("  Added:\n")
				for _, svc := range result.AddedServices {
					fmt.Printf("    - %s\n", svc)
				}
			}
			if len(result.TotalServices) > 0 {
				fmt.Printf("  Total services:\n")
				for _, svc := range result.TotalServices {
					fmt.Printf("    - %s\n", svc)
				}
			}
		}
		return nil
	}

	// Standard create/update case
	if result.Action == "updated" {
		fmt.Printf("Updated mock: %s\n", created.ID)
	} else {
		fmt.Printf("Created mock: %s\n", created.ID)
	}
	fmt.Printf("  Type: %s\n", created.Type)

	switch mockType { //nolint:exhaustive // only protocol types with specific output
	case mock.TypeHTTP:
		if created.HTTP != nil {
			if created.HTTP.Matcher != nil {
				fmt.Printf("  Method: %s\n", created.HTTP.Matcher.Method)
				fmt.Printf("  Path:   %s\n", created.HTTP.Matcher.Path)
			}
			switch {
			case created.HTTP.StatefulBinding != nil:
				fmt.Printf("  Table: %s\n", created.HTTP.StatefulBinding.Table)
				fmt.Printf("  Bind:  %s\n", created.HTTP.StatefulBinding.Action)
				if created.HTTP.StatefulBinding.Operation != "" {
					fmt.Printf("  Operation: %s\n", created.HTTP.StatefulBinding.Operation)
				}
			case created.HTTP.StatefulOperation != "":
				fmt.Printf("  Operation: %s\n", created.HTTP.StatefulOperation)
			case created.HTTP.Response != nil:
				fmt.Printf("  Status: %d\n", created.HTTP.Response.StatusCode)
			}
		}
	case mock.TypeWebSocket:
		if created.WebSocket != nil {
			fmt.Printf("  Path: %s\n", created.WebSocket.Path)
			if created.WebSocket.EchoMode != nil && *created.WebSocket.EchoMode {
				fmt.Printf("  Echo: enabled\n")
			}
		}
	case mock.TypeGRPC:
		if created.GRPC != nil {
			for svc, cfg := range created.GRPC.Services {
				fmt.Printf("  Service: %s\n", svc)
				for method := range cfg.Methods {
					fmt.Printf("  Method: %s\n", method)
				}
			}
		}
	}

	return nil
}

// outputJSONResult outputs the created or merged mock in JSON format.
func outputJSONResult(result *CreateMockResult, mockType mock.Type) error { //nolint:gocyclo // per-type JSON output formatting
	created := result.Mock

	// For merge results, include merge-specific information
	if result.IsMerge() {
		switch mockType { //nolint:exhaustive // only gRPC has merge JSON output
		case mock.TypeGRPC:
			return output.JSON(struct {
				ID            string   `json:"id"`
				Type          string   `json:"type"`
				Action        string   `json:"action"`
				AddedServices []string `json:"addedServices,omitempty"`
				TotalServices []string `json:"totalServices,omitempty"`
			}{
				ID:            created.ID,
				Type:          string(created.Type),
				Action:        result.Action,
				AddedServices: result.AddedServices,
				TotalServices: result.TotalServices,
			})
		}
	}

	// Standard create output
	switch mockType { //nolint:exhaustive // only protocol types with specific JSON output
	case mock.TypeHTTP:
		createdMethod := ""
		createdPath := ""
		createdStatus := 0
		createdOperation := ""
		createdTable := ""
		createdBind := ""
		if created.HTTP != nil && created.HTTP.Matcher != nil {
			createdMethod = created.HTTP.Matcher.Method
			createdPath = created.HTTP.Matcher.Path
		}
		if created.HTTP != nil && created.HTTP.Response != nil {
			createdStatus = created.HTTP.Response.StatusCode
		}
		if created.HTTP != nil {
			createdOperation = created.HTTP.StatefulOperation
		}
		if created.HTTP != nil && created.HTTP.StatefulBinding != nil {
			createdTable = created.HTTP.StatefulBinding.Table
			createdBind = created.HTTP.StatefulBinding.Action
			if created.HTTP.StatefulBinding.Operation != "" {
				createdOperation = created.HTTP.StatefulBinding.Operation
			}
		}
		return output.JSON(struct {
			ID                string `json:"id"`
			Type              string `json:"type"`
			Action            string `json:"action"`
			Method            string `json:"method"`
			Path              string `json:"path"`
			StatusCode        int    `json:"statusCode,omitempty"`
			StatefulOperation string `json:"statefulOperation,omitempty"`
			Table             string `json:"table,omitempty"`
			Bind              string `json:"bind,omitempty"`
		}{
			ID:                created.ID,
			Type:              string(created.Type),
			Action:            result.Action,
			Method:            createdMethod,
			Path:              createdPath,
			StatusCode:        createdStatus,
			StatefulOperation: createdOperation,
			Table:             createdTable,
			Bind:              createdBind,
		})

	case mock.TypeWebSocket:
		wsPath := ""
		echoEnabled := false
		if created.WebSocket != nil {
			wsPath = created.WebSocket.Path
			if created.WebSocket.EchoMode != nil {
				echoEnabled = *created.WebSocket.EchoMode
			}
		}
		return output.JSON(struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Action string `json:"action"`
			Path   string `json:"path"`
			Echo   bool   `json:"echo"`
		}{
			ID:     created.ID,
			Type:   string(created.Type),
			Action: result.Action,
			Path:   wsPath,
			Echo:   echoEnabled,
		})

	case mock.TypeGRPC:
		var services []string
		var methods []string
		if created.GRPC != nil {
			for svc, cfg := range created.GRPC.Services {
				services = append(services, svc)
				for method := range cfg.Methods {
					methods = append(methods, method)
				}
			}
		}
		return output.JSON(struct {
			ID       string   `json:"id"`
			Type     string   `json:"type"`
			Action   string   `json:"action"`
			Services []string `json:"services"`
			Methods  []string `json:"methods"`
		}{
			ID:       created.ID,
			Type:     string(created.Type),
			Action:   result.Action,
			Services: services,
			Methods:  methods,
		})

	}

	// Fallback for unknown types
	return output.JSON(created)
}
