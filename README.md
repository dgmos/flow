# Dgmos Flow

A workflow orchestration engine for Go applications. Provides DAG-based execution, distributed task processing, and durable state management.

## Installation

```bash
go get github.com/dgmos/flow
```

All dependencies (database drivers, Redis client, etc.) are automatically installed.

## Requirements

- Go 1.23 or higher
- MySQL 5.7+, PostgreSQL 12+, or Redis 6+ (optional - in-memory storage available)

## Quick Start

```go
package main

import (
    "context"
    "github.com/dgmos/flow"
    _ "github.com/go-sql-driver/mysql"
    "github.com/jmoiron/sqlx"
)

func main() {
    // Initialize storage
    db, _ := sqlx.Connect("mysql", "user:pass@tcp(localhost:3306)/db?parseTime=true")
    storage, _ := flow.NewMySQLStorage(db)
    
    // Create engine and registry
    registry := flow.NewRegistry()
    engine := flow.NewEngine(storage, registry)
    
    // Register function
    registry.RegisterFunction("ProcessData", func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
        // Your logic here
        return map[string]interface{}{"status": "processed"}, nil
    })
    
    // Define workflow
    wf := flow.NewWorkflow("process-workflow", "Data Processing").
        AddNode("process", flow.NodeTypeFunction).
            WithConfig("function", "ProcessData").
        Build()
    
    // Register and execute
    ctx := context.Background()
    engine.RegisterWorkflow(ctx, "default", wf)
    
    // Start worker
    go engine.StartWorker(ctx, "default", "worker-1")
    
    // Execute workflow
    execID, _ := engine.Execute(ctx, "default", "process-workflow", []byte(`{"data":"value"}`))
    
    // Query status
    execution, _ := engine.GetExecution(ctx, "default", execID)
}
```

## Storage Backends

### MySQL

```go
db, _ := sqlx.Connect("mysql", "user:pass@tcp(host:3306)/db?parseTime=true")
storage, _ := flow.NewMySQLStorage(db)
```

Database schema is created automatically on first connection.

### PostgreSQL

```go
db, _ := sqlx.Connect("postgres", "postgres://user:pass@host/db?sslmode=disable")
storage, _ := flow.NewPostgreSQLStorage(db)
```

### Redis

```go
storage := flow.NewRedisStorage("localhost:6379", "", 0)
```

### In-Memory

```go
storage := flow.NewMemoryStorage()
```

Suitable for development and testing.

## API Reference

### Engine

```go
engine := flow.NewEngine(storage, registry)
engine.WithLogger(logger)
engine.RegisterWorkflow(ctx, namespace, workflow)
engine.Execute(ctx, namespace, workflowID, input)
engine.GetExecution(ctx, namespace, executionID)
engine.GetExecutionSteps(ctx, namespace, executionID)
engine.CancelExecution(ctx, namespace, executionID)
engine.FailExecution(ctx, namespace, executionID, message)
engine.SignalExecution(ctx, namespace, executionID, nodeID, data)
engine.StartWorker(ctx, namespace, workerID)
```

### Registry

```go
registry := flow.NewRegistry()
registry.RegisterFunction(name, handlerFunc)
registry.Register(nodeType, executor)
```

### Workflow Builder

```go
wf := flow.NewWorkflow(id, name).
    AddNode(nodeID, nodeType).
        WithConfig(key, value).
        WithRetry(count).
        WithSaga(compensateNodeID).
    Connect(sourceID, targetID).
    ConnectIf(sourceID, targetID, condition).
    Build()
```

### Worker

```go
worker := flow.NewWorker(workerID, engine, registry, interval)
worker.SetNamespace(namespace)
worker.SetConcurrency(16) // run up to 16 steps in parallel (default 1)
worker.Start(ctx)         // blocks until ctx is cancelled

// or
go engine.StartWorkerWithOptions(ctx, "default", "worker-1", flow.WorkerOptions{
    Concurrency:  16,
    PollInterval: 100 * time.Millisecond, // idle polling; busy workers claim immediately
})
```

On shutdown (cancel `ctx`) the worker stops claiming, cancels in-flight steps,
puts them back to `PENDING` without consuming a retry attempt, and returns once
they are released.

## Node Types

Built-in node types are automatically registered:

- `function` - Execute registered Go functions
- `http` - HTTP requests
- `condition` - Conditional branching
- `log` - Logging
- `transform` - Data transformation
- `signal` - Wait for external signals
- `subworkflow` - Nested workflows
- `ai` - LLM API calls
- `notify` - Notifications
- `switch` - Multi-way branching
- `wait` - Delay execution
- `setvariable` - Context manipulation

## n8n Workflows

Import and run [n8n](https://n8n.io) workflow exports and templates. Every n8n node becomes a durable engine step; expressions, items, paired items, loops, sub-workflows, AI agents (OpenAI, Anthropic, Gemini, …), RAG (Qdrant, Pinecone, Supabase) and 90+ integrations are supported. Across the whole public n8n gallery (14,314 templates), 86% import and run; on the 299 most-viewed templates, all import and run (297 fully; 2 contain unfilled placeholders that make one node fail, exactly as in n8n). The importer reports exactly what is missing for any workflow.

```go
eng.EnableN8N(flow.N8NOptions{Credentials: myVault})
res, err := eng.InstallN8NWorkflow(ctx, "default", workflowJSON, flow.N8NInstallOptions{})
http.Handle("/n8n/", http.StripPrefix("/n8n", eng.N8NHandler(flow.N8NHandlerOptions{Namespace: "default"})))
```

See [docs/n8n.md](docs/n8n.md) for the full guide (supported nodes, endpoints, credentials, security).

## Configuration

### Retry Policy

```go
node := flow.Node{
    ID: "task",
    Type: flow.NodeTypeFunction,
    RetryPolicy: &flow.RetryPolicy{
        MaxAttempts:        5,
        InitialInterval:    time.Second,
        BackoffCoefficient: 2.0,
        MaximumInterval:    time.Minute,
    },
}
```

`MaxAttempts` is the number of retries after the first failure. The delay
before retry *n* is `InitialInterval * BackoffCoefficient^(n-1)`, capped at
`MaximumInterval`.

Errors that can never succeed are not retried: return an error implementing
`flow.PermanentError` (`Permanent() bool`) from a custom node to fail the step
immediately. Outbound policy violations and invalid node config are permanent.

### Step Timeout

Any node can set `execution_timeout` (Go duration). The executor's context is
cancelled when it expires and the attempt fails (and is retried per policy):

```go
builder.AddNode("call", flow.NodeTypeHTTP).
    WithConfig("url", "https://api.example.com/slow").
    WithConfig("execution_timeout", "45s")
```

### Outbound Network Policy (SSRF protection)

The `http` and `notify` nodes may only reach **public internet addresses** by
default. Loopback, private (RFC 1918 / ULA), link-local (including the
`169.254.169.254` cloud metadata endpoint) and CGNAT ranges are blocked; the
check runs on the resolved IP for every connection, so redirects and DNS
rebinding cannot bypass it. Only `http`/`https` URLs are allowed, responses
are capped at 10 MiB, and environment proxy settings are ignored.

To reach internal services, allow them explicitly:

```go
engine.SetOutboundPolicy(flow.OutboundPolicy{
    AllowedHosts:     []string{"billing.internal", "10.20.0.0/16"},
    MaxResponseBytes: 50 << 20, // 50 MiB; -1 = unlimited
    Timeout:          time.Minute,
})

// or, only when every workflow definition is fully trusted:
engine.SetOutboundPolicy(flow.OutboundPolicy{AllowPrivateNetworks: true})
```

Blocked requests fail with `flow.ErrDestinationBlocked` (`errors.Is`).

### Compensation (Saga Pattern)

```go
builder.AddNode("charge", flow.NodeTypeFunction).
    WithSaga("refund")
```

### Encryption

```go
// Key must be 32 bytes encoded as 64 hex characters
crypto, _ := flow.NewCrypto("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
storage.(*sql.SQLStorage).SetEncryption(crypto)
```

## Triggers

### Cron

```go
_ = engine.CronManager.Start(ctx)
id, _ := engine.CronManager.AddScheduleWithOpts(ctx, namespace, workflowID, "0 0 9 * * *", input, flow.ScheduleOpts{
    Name: "daily-backup", Timezone: "Europe/Istanbul", CatchUp: true,
})
expr := "0 30 9 * * *"
_ = engine.CronManager.UpdateSchedule(ctx, namespace, id, flow.ScheduleUpdateOpts{CronExpression: &expr})
_ = engine.CronManager.PauseSchedule(ctx, namespace, id)  // soft disable
_ = engine.CronManager.RemoveSchedule(ctx, namespace, id) // hard delete
api := flow.NewAPIManager(webhookMgr).WithCron(engine.CronManager)
```

Multi-replica safe (atomic fire claim). Pause = soft disable; DELETE = hard remove. Table: `schedules` (or `{prefix}schedules`).

### Webhooks

```go
webhookMgr := flow.NewWebhookManager(engine)
http.Handle("/webhooks/", webhookMgr)
```

### REST API

```go
apiMgr := flow.NewAPIManager(webhookMgr)
http.Handle("/", apiMgr)
```

Endpoints:
- `GET /health` - Health check
- `GET /metrics` - Prometheus metrics
- `GET /api/{namespace}/executions` - List executions
- `GET /api/{namespace}/executions/{id}` - Get execution
- `GET /api/{namespace}/executions/{id}/steps` - Get execution steps
- `POST /api/{namespace}/executions/{id}/cancel` - Cancel execution
- `POST /api/{namespace}/executions/{id}/signal/{nodeID}` - Send signal
- `POST /api/{namespace}/executions/{id}/rollback` - Start saga rollback
- `POST /api/{namespace}/executions/{id}/tasks/{nodeId}/claim|complete|advance` - Human tasks
- `GET /api/{namespace}/tasks` - List waiting human tasks

## Events

Engine emits lifecycle events via `SetHostHooks` (sync) and/or `SetEventBus`.

```go
engine.SetHostHooks(handler) // WS / notification bridge

eventBus := flow.NewEventBus()
eventBus.RegisterHandler(flow.EventWorkflowCompleted, handler)
eventBus.RegisterGlobalHandler(handler)
engine.SetEventBus(eventBus)
```

Event types include:
- `EventWorkflowStarted` / `Completed` / `Failed` / `Cancelled`
- `EventStepStarted` / `Completed` / `Failed` / `Retrying`
- `EventStepWaitingSignal` / `EventStepWaitingHuman`
- `EventSignalReceived` / `EventTaskClaimed` / `EventTaskCompleted`
- `EventCompensationTriggered` / `EventRollbackStarted`

## Interactive & Human Tasks

```go
_, next, err := engine.CompleteTask(ctx, ns, execID, "approve", "user-1", "approve", `{"note":"ok"}`)
out, next, err := engine.Advance(ctx, ns, execID, "step-2", "submit", `{}`)
tasks, _ := engine.ListTasks(ctx, ns, flow.TaskFilter{Assignee: "user-1"})
```

## Variables

```go
_ = engine.MergeVars(ctx, ns, execID, map[string]interface{}{"process_data": data})
```

## Authorization

Dgmos Flow provides a flexible, pluggable authorization system. Bring your own auth!

Recommended resources: `workflow:{ns}:{id}`, `execution:{ns}:{id}`, `task:{ns}:{stepId}`.

### Interface

```go
type AuthProvider interface {
    CheckAccess(ctx context.Context, resource string, action string) error
}
```

### Usage

#### 1. No Auth (Development)

```go
engine := flow.NewEngine(storage, registry)
// No auth provider set - everything is allowed
```

#### 2. Simple Custom Auth

```go
type MyAuth struct{}

func (a *MyAuth) CheckAccess(ctx context.Context, resource string, action string) error {
    userID := ctx.Value("user_id").(string)
    
    // Your custom logic
    if userID == "" {
        return errors.New("unauthorized")
    }
    
    // Check permissions in your database, Firebase, Auth0, etc.
    return nil
}


engine := flow.NewEngine(storage, registry)
engine.SetAuthProvider(&MyAuth{})
```

#### 3. Multi-Tenant with Customer Isolation

```go
type MultiTenantAuth struct{}

func (a *MultiTenantAuth) CheckAccess(ctx context.Context, resource string, action string) error {
    customerID := ctx.Value("customer_id").(string)
    userID := ctx.Value("user_id").(string)
    role := ctx.Value("role").(string)
    
    // Get workflow metadata
    workflow := getWorkflowFromDB(resource)
    
    // CRITICAL: Tenant isolation
    if workflow.Metadata["customer_id"] != customerID {
        return errors.New("forbidden: wrong tenant")
    }
    
    // Admin can do anything
    if role == "admin" {
        return nil
    }
    
    // Owner can do anything
    if workflow.Metadata["owner_id"] == userID {
        return nil
    }
    
    // Check visibility
    if workflow.Metadata["visibility"] == "organization" {
        if action == "view" || action == "execute" {
            return nil
        }
    }
    
    return errors.New("forbidden")
}

engine.SetAuthProvider(&MultiTenantAuth{})
```

#### 4. Store Auth Metadata in Workflows

```go
wf := &flow.Workflow{
    ID:   "my-workflow",
    Name: "My Workflow",
    Metadata: map[string]interface{}{
        // Use ANY field names you want - we don't enforce structure
        "customer_id": "acme-corp",     // Multi-tenant isolation
        "user_id":     "user-123",      // Owner
        "owner":       "john@acme.com", // Email-based
        "slug":        "acme/my-wf",    // Slug-based
        "team_id":     "eng-team",      // Team access
        "visibility":  "organization",  // Visibility level
        
        // Or use your own auth system fields
        "firebase_uid": "abc123",
        "auth0_org":    "org_xyz",
    },
}

// Add auth context when registering
ctx = context.WithValue(ctx, "customer_id", "acme-corp")
ctx = context.WithValue(ctx, "user_id", "user-123")
engine.RegisterWorkflow(ctx, "default", wf)
```

### Actions

Common actions (you can define your own):
- `"create"` - Create/register workflow
- `"view"` - View workflow definition
- `"execute"` - Trigger execution
- `"edit"` - Modify workflow
- `"delete"` - Delete workflow

### Key Principles

1. **Fully pluggable** - Use any auth system (Firebase, Auth0, custom DB, etc.)
2. **No prescribed structure** - Use `user_id`, `userId`, `email`, `slug`, or whatever you want
3. **Context-based** - Pass auth info via `context.Context`
4. **Metadata is flexible** - Store any auth-related data in `workflow.Metadata`

### Complete Example

See `examples/auth/` for a full multi-tenant implementation with:
- Customer/tenant isolation
- Role-based access control (admin, user, viewer)
- Team-based visibility
- Organization-wide workflows

## Monitoring

### Prometheus Metrics

```go
import "github.com/prometheus/client_golang/prometheus/promhttp"

http.Handle("/metrics", promhttp.Handler())
```

Available metrics:
- `workflows_started_total`
- `workflows_completed_total`
- `workflows_failed_total`
- `steps_processed_total`
- `step_duration_seconds`
- `active_workers`

### Logging

```go
import "log/slog"

logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
engine.WithLogger(logger)
```

## Testing

```bash
# Unit tests (default)
go test

# Unit tests with coverage
go test -cover

# Race detector
go test -race

# Integration / E2E (requires Docker: MySQL, PostgreSQL, Redis)
bash scripts/test-integration.sh
# or manually:
# docker compose up -d mysql postgres redis
# go test -tags=integration ./... -timeout=10m

# Full local suite (unit + stability + benchmarks)
bash scripts/test-full.sh
```

## Examples

See `examples/` directory for complete working examples:
- `examples/auth/` - Multi-tenant authorization
- `examples/interactive_approval/` - Human task approval flow

## Database Schema

Tables are created automatically when using SQL storage backends:

- `workflows` - Workflow definitions
- `executions` - Workflow execution instances (includes `variables`)
- `execution_steps` - Individual task states (includes `task_meta`)
- `compensation_journal` - Saga/rollback audit trail
- `schedules` - Durable cron schedules

Indexes are optimized for:
- Namespace-based queries
- Status filtering
- Worker task claims
- Execution lookups

## Concurrency Model

- Workers claim tasks using database-level locking (`SKIP LOCKED`)
- Multiple workers can run concurrently across different processes/hosts
- Running tasks hold a lease that the worker renews every 30s; a task is reassigned only after its lease has been silent for 5 minutes (worker crash), so long-running tasks are not executed twice
- Join nodes (multiple incoming edges) are scheduled exactly once, even when parallel branches finish at the same time
- No message broker required - coordination through storage backend

## Error Handling

- Failed tasks can be retried with configurable policies
- Saga pattern supported for compensation logic
- Workflow status tracked: `PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `CANCELLED`
- Task status tracked: `PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `SKIPPED`, `WAITING`, `CANCELLED`

## License

MIT License. See [LICENSE](LICENSE) file.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Documentation

Full API documentation: https://pkg.go.dev/github.com/dgmos/flow
