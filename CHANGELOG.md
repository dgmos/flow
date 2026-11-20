# Changelog

All notable changes to this project are documented here.

## [Unreleased]

This release fixes several correctness bugs in scheduling and storage, and
hardens the engine for running untrusted workflow definitions (e.g. imported
templates). **It contains behavior changes that need action before deploying**;
read the [Upgrade guide](#upgrade-guide) first.

### n8n compatibility

- **Import and run n8n workflows** (`EnableN8N`, `ImportN8NWorkflow`, `InstallN8NWorkflow`, `N8NHandler`). Each n8n node runs as a durable engine step with n8n's item model, paired-item tracing and `{{ }}` expressions (sandboxed JavaScript with n8n's helpers and a Luxon-compatible `DateTime`).
- 86 node types plus AI/RAG sub-nodes: flow & data nodes, Code, loops (Loop Over Items and polling cycles), sub-workflows, HTTP Request, Telegram, Gmail, Google Sheets/Drive/Calendar/Docs, Slack, Discord, WhatsApp, Notion, Airtable, WordPress, X, LinkedIn, Outlook, Postgres, MySQL, Redis, Supabase, SMTP, file utilities; AI Agent with tools/memory/output parsers on OpenAI, Anthropic, Gemini and OpenAI-compatible providers; RAG with Qdrant, Pinecone, Supabase and in-memory stores; any supported integration usable as an AI tool.
- Webhook (all response modes), chat, form, Telegram/WhatsApp, schedule and polling (Gmail/Sheets/Drive) triggers; human-in-the-loop via signed resume URLs (Wait on webhook, send-and-wait approvals, multi-page forms).
- Compatibility report per workflow (blocking issues, warnings, credentials, triggers) and a corpus benchmark (`-tags n8ncorpus`): all 299 most-viewed public templates import and run (297 fully; 2 contain unfilled placeholders that make one node fail, as in n8n). Invalid expressions are warnings, as n8n only fails when that node runs; unescaped quotes in node references are repaired.
- All outbound traffic of n8n nodes (HTTP, AI providers, SMTP, databases) is subject to the SSRF policy; `$env` and file access are opt-in.
- Hardening for untrusted workflows: credentials are bound to their service domains (`allowedDomains`), JavaScript runs cannot hang a worker (abandoned runs, regex match timeouts, recursion limit), approval links sign the answer and require a POST confirmation, resume links are node-scoped, Respond to Webhook cannot set cookies/HSTS, webhook paths cannot be hijacked by another workflow, email headers cannot be injected, credential headers are dropped on cross-host redirects, secrets are redacted from errors, IPv6 transition ranges (NAT64/6to4/Teredo) are blocked, file sandbox refuses symlinks, connection pools are bounded and keyed by credentials, the in-memory vector store is per namespace.
- Polling triggers: overlapping polls of the same trigger are serialized, and stores implementing `N8NPollStateSwapper` advance cursors with compare-and-swap so polls on several instances never emit duplicates.
- `DateTime.diff` in months/quarters/years now follows Luxon's calendar arithmetic; quotes and braces inside regex literals no longer break `{{ }}` template splitting.
- Split In Batches v1/v2 loops (node context `noItemsLeft`), HTTP Request pagination, Remove Duplicates history modes (incremental, date, workflow scope), Compression, Read PDF, Crypto and Execution Data nodes, `$jmespath`, Google Drive v1/v2, YouTube, Email Trigger (IMAP), XLSX/ODS reading and XLSX writing.
- Move Binary Data, Compare Datasets, AI Transform, Debug Helper, Customer Datastore (training), Respond to Chat, Chat Memory Manager, MySQL table operations, Google Sheets v1–v3, summarization with a document loader, vector store update mode (Supabase, Qdrant, Pinecone); Code nodes get `this.helpers` (httpRequest, request, prepareBinaryData, getBinaryDataBuffer, returnJsonArray) and `require('crypto')` / `require('util')`.
- Integrations: GitHub, Hacker News, Reddit, Twilio (+ Event Streams trigger), OpenWeatherMap, urlscan.io, APITemplate.io, uProc, WooCommerce, Microsoft OneDrive, Excel 365, Teams, Outlook trigger (polling), Baserow, NocoDB, Odoo, Jira, Ghost, Dropbox, Google Tasks, Google Analytics (GA4), S3 (SigV4, any S3-compatible endpoint) and MongoDB (driver v2.8.2); each is also usable as an AI tool.
- AI: Azure OpenAI, Ollama (LLM), Hugging Face Inference models; PGVector and MongoDB Atlas vector stores; Cohere reranker; LangChain Code node (Execute mode) with a LangChain JS subset (prompt templates, messages, output parsers, ChatOpenAI/OpenAIEmbeddings, QdrantVectorStore) and the connected chat models.
- MCP: Server Trigger (Streamable HTTP and SSE at `/mcp/{path}`, each tool call is an execution), MCP Client Tool for agents, community MCP Client node (STDIO only with `Options.AllowLocalProcesses`).
- Opt-in local execution (`N8NOptions.AllowLocalProcesses`, off by default): Execute Command, Python Code (Pyodide-style globals on a local `python3`, `N8NOptions.PythonPath`) and STDIO MCP servers. Local File Trigger (polling, inside `FileSystemRoot`).
- Community nodes: Tavily, ScrapeNinja, InfraNodus, Blotato, YouTube Transcript (also `require('youtube-transcript')` in Code); `{placeholder}` arguments of community AI tools.
- Data Table node (row insert/get/update/upsert/delete/exists, table create/list/clear/rename/delete) on a pluggable `N8NOptions.DataTables` store; tables referenced by imported templates are created on first use. HubSpot (v1–v2.x: contacts, companies, deals, engagements, tickets, contact lists, owners), Google Gemini and Anthropic vendor nodes, Perplexity, Apify, AI Agent Tool, and tool variants of the Gemini/Anthropic/OpenAI vendor nodes.
- Whole-gallery benchmark: `scripts/fetch-n8n-templates.sh` downloads all public templates; 10,198 of 14,314 (71.2%) import and run. The report shows the cumulative share each blocker unlocks and groups blocked templates by what they need. Unsupported operations of partially implemented nodes are reported at import.

### Engine

- **Compare-and-set state transitions** (`storage.ConditionalUpdater`, implemented by memory, SQL and Redis): two concurrent resumes of the same waiting step can no longer both apply (`ErrStepConflict`); a reclaimed worker's late result is discarded and its executor is cancelled when the lease is lost; the timeout reaper cannot overwrite a resumed step; finishing, failing and cancelling an execution cannot overwrite each other.
- Steps of failed, completed or cancelled executions are no longer run (compensation steps still are).
- Redis: step creation, updates and claims are atomic Lua scripts; claims never revive finished or waiting steps.
- `CompleteStep` keeps scheduling children even if the caller's context is cancelled; a periodic sweep finalizes executions left RUNNING after a crash.
- Durable timers (`WaitUntilError`): executors can pause until a time without holding a worker.
- Port-aware routing: nodes can emit to several outputs at once (`{"__ports": {...}}`) and receive per-input data (`InputBuilder`, `Edge.TargetInput`).
- Fixed a race where a worker finishing the last branch could complete an execution while another worker was about to schedule a join step, leaving that step orphaned.

### Security

- **SSRF protection for `http` and `notify` nodes.** By default they can only
  reach public internet addresses. Loopback, private (RFC 1918 / ULA),
  link-local (including the `169.254.169.254` cloud metadata endpoint), CGNAT
  and multicast ranges are blocked. The check runs on the resolved IP of every
  connection, so redirects and DNS rebinding cannot bypass it. Only
  `http`/`https` URLs are allowed. Configure with `Engine.SetOutboundPolicy`.
- HTTP response bodies are capped at 10 MiB (`OutboundPolicy.MaxResponseBytes`).
- `FailExecution` and `GetExecutionSteps` now go through the `AuthProvider`
  (actions `fail` and `view` on `execution:<ns>:<id>`).

### Fixed

- **SQL: workers claimed steps from other namespaces.** An operator-precedence
  bug in `ClaimReadyStep` applied the namespace filter only to stale RUNNING
  steps, so a worker pinned to one namespace also ran PENDING steps of every
  other namespace.
- **Long-running steps were executed twice.** Steps running longer than
  5 minutes were reclaimed by another worker. Workers now renew a lease every
  30s; a step is reclaimed only after 5 minutes without a heartbeat.
- **Join nodes could run twice or never.** When parallel branches finished at
  the same time, the join node could be scheduled twice. Steps now have
  deterministic IDs per (execution, node), so duplicates are rejected.
- **Wrong skipping at merge points.** A node fed by an untaken IF/switch branch
  *and* by another branch was skipped even when the other branch completed. A
  node now runs when at least one incoming edge is active and is skipped only
  when all incoming edges are dead.
- **Redis: retries never ran.** A step rescheduled for retry was not put back
  in the queue.
- **Redis: claimed steps were lost** if a worker crashed right after claiming,
  and Redis errors during claim were silently ignored.
- **Cyclic workflows were accepted** and hung. `RegisterWorkflow`,
  `StartWorkflow` and `ImportStepChain` now reject cycles.
- `StartWorkflow` left an orphan RUNNING execution when node validation failed.
- A cancelled execution could be overwritten as COMPLETED by a finishing step.
- Finishing an execution could overwrite execution variables with stale data.
- Compensation step input was invalid JSON when the error contained quotes.
- Compensation step and journal IDs (`comp-…`, `jrnl-…`) exceeded the
  36-character SQL `id` column, so they failed to save on SQL backends.
- Retry backoff is now exponential as documented:
  `InitialInterval * BackoffCoefficient^(n-1)`, capped at `MaximumInterval`.
- Condition node: numbers are compared numerically for `==`/`!=`
  (`5 == "5.0"`), ordering no longer accepts partial numbers
  (`"10 apples" > 5` is false), and unknown operators fail validation.
- In-memory storage returned internal pointers, causing data races.
- Possible nil-pointer panics in the worker when an execution or workflow
  could not be loaded.

### Added

- `Engine.StartWorkerWithOptions` / `Worker.SetConcurrency`: process steps in
  parallel (`WorkerOptions{Concurrency, PollInterval}`). Busy workers claim the
  next step immediately instead of waiting for the poll interval.
- Graceful worker shutdown: on context cancellation in-flight steps are
  released back to PENDING without consuming a retry attempt, and `Start`
  returns once they are released.
- `execution_timeout` node config (Go duration, e.g. `"45s"`).
- `PermanentError`: executor errors implementing `Permanent() bool` fail the
  step without retries. Policy violations and invalid config are permanent.
- `OutboundPolicy`, `Engine.SetOutboundPolicy`, `ErrDestinationBlocked`,
  `ErrResponseTooLarge`.
- Storage: `storage.ErrStepExists` and the optional `StepHeartbeater`
  interface (implemented by the memory, SQL and Redis backends).

### Changed

- New dependencies for n8n support: goja (JavaScript), regexp2, goquery, goldmark, html-to-markdown and a PDF text extractor (all pinned to versions that keep the Go 1.23 requirement).
- Prometheus: `flow_workflows_failed_total` label `error` is renamed to
  `failed_node` and now holds the failed node ID (the raw error message caused
  unbounded label cardinality).
- Execution `Output` is the output of the most recently finished completed
  step (was: whichever step storage returned last).
- Join node input (multiple predecessors) contains only predecessors whose
  edge was taken; skipped predecessors are omitted instead of `null`.
- Tooling: golangci-lint config migrated to v2 and pinned in CI; `gofmt`
  enforced; CI now runs the tests of every package.

## Upgrade guide

No database migration is needed. Executions that are in progress during the
upgrade continue with the new version.

### Required before deploying

1. **Allow internal HTTP destinations.** Any workflow whose `http` or `notify`
   node calls `localhost`, a private IP or an internal hostname will fail with
   `ErrDestinationBlocked`. Allow them explicitly after `NewEngine`:

   ```go
   engine.SetOutboundPolicy(flow.OutboundPolicy{
       AllowedHosts: []string{"billing.internal", "10.20.0.0/16"},
   })
   ```

   Use `AllowPrivateNetworks: true` only if every workflow definition is fully
   trusted.

2. **Check for egress proxies.** `HTTP_PROXY` / `HTTPS_PROXY` are now ignored by
   the `http` and `notify` nodes (a proxy would bypass the destination check).
   If production reaches the internet only through a proxy, these nodes will
   fail. Proxy support is not available yet — do not deploy until it is added.

3. **Update dashboards and alerts** that use the `error` label of
   `flow_workflows_failed_total`:

   ```promql
   # before
   sum by (error) (rate(flow_workflows_failed_total[5m]))
   # after
   sum by (failed_node) (rate(flow_workflows_failed_total[5m]))
   ```

   Alerts that filter on `error` will silently stop firing.

4. **Upgrade all workers at once.** Old and new workers must not run side by
   side: old workers still have the cross-namespace claim bug and do not send
   heartbeats. Stop old workers before starting new ones.

### Check if it applies to you

- **Custom `AuthProvider` with an action allow-list:** allow the new `fail`
  action for callers of `FailExecution`, and make sure `view` on
  `execution:<ns>:<id>` is allowed for code that calls
  `Engine.GetExecutionSteps` directly (the REST steps endpoint already
  required it).
- **Large HTTP responses:** responses over 10 MiB now fail the step. Raise
  `OutboundPolicy.MaxResponseBytes` (`-1` = unlimited).
- **Merge-point workflows:** nodes fed by an untaken branch and another branch
  now run instead of being skipped. Review workflows that relied on the old
  behavior.
- **Retry timing:** with the default policy retries now happen after 10s, 20s
  and 40s (previously 20s, 40s and 60s).
- **Compensation IDs:** compensation step and journal IDs are UUIDs; code that
  matched the `comp-` or `jrnl-` prefixes must change.
- **Condition node:** workflows with an unknown operator are now rejected at
  start instead of failing at run time.
- **Custom `Storage` implementations:** `CreateExecutionStep` must return
  `storage.ErrStepExists` when a step with the same ID already exists — it must
  not overwrite it. Optionally implement `storage.StepHeartbeater` so
  long-running steps are not reclaimed.

### After deploying (Redis backend only)

Steps that were rescheduled for retry under the previous version were never
re-queued and stay `PENDING` forever. They are not recovered automatically.
Find executions in `RUNNING` with a `PENDING` step that is missing from the
`pending:<namespace>` sorted set and either re-queue the step
(`ZADD pending:<namespace> <unix-ts> <step-id>`) or cancel the execution.
