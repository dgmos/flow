package n8n

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dgmos/flow/internal/engine"
)

var errWaitSignal = errors.New(engine.WaitSignal)

// CredentialRef identifies a credential in an n8n node ({id, name}). The
// engine also fills in which workflow and node ask for it, so providers can
// enforce which workflows may use which credentials.
type CredentialRef struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	WorkflowID string `json:"workflowId,omitempty"`
	Node       string `json:"node,omitempty"`
}

// CredentialProvider resolves n8n credentials for a namespace. The returned
// map holds the credential's fields as n8n defines them for its type, e.g.
// openAiApi: {"apiKey": "..."}, httpHeaderAuth: {"name": "...", "value": "..."},
// any OAuth2 type: {"accessToken": "..."} (or n8n's {"oauthTokenData": {...}}).
type CredentialProvider interface {
	Credential(ctx context.Context, namespace, credType string, ref CredentialRef) (map[string]interface{}, error)
}

// StaticCredentials is a simple in-memory CredentialProvider keyed by
// credential type and then by ID or name. Use it for tests and single-tenant
// setups; production hosts should back CredentialProvider with a vault.
type StaticCredentials struct {
	mu    sync.RWMutex
	creds map[string]map[string]map[string]interface{}
}

// NewStaticCredentials returns an empty StaticCredentials.
func NewStaticCredentials() *StaticCredentials {
	return &StaticCredentials{creds: map[string]map[string]map[string]interface{}{}}
}

// Set stores a credential under its type for the given ID or name. The
// special key "*" matches any reference of that type.
func (s *StaticCredentials) Set(credType, idOrName string, data map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds[credType] == nil {
		s.creds[credType] = map[string]map[string]interface{}{}
	}
	s.creds[credType][idOrName] = data
}

// Credential implements CredentialProvider.
func (s *StaticCredentials) Credential(_ context.Context, _ string, credType string, ref CredentialRef) (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byType := s.creds[credType]
	for _, k := range []string{ref.ID, ref.Name, "*"} {
		if k == "" {
			continue
		}
		if c, ok := byType[k]; ok {
			return c, nil
		}
	}
	return nil, fmt.Errorf("credential %s %q not found", credType, ref.Name)
}

// nodeTypes lists every supported n8n node type.
func nodeTypes() map[string]nodeFunc {
	b := "n8n-nodes-base."
	return map[string]nodeFunc{
		// triggers
		b + "manualTrigger":                          passthrough,
		b + "start":                                  passthrough,
		b + "webhook":                                passthrough,
		b + "formTrigger":                            passthrough,
		b + "executeWorkflowTrigger":                 passthrough,
		b + "errorTrigger":                           passthrough,
		b + "telegramTrigger":                        passthrough,
		b + "whatsAppTrigger":                        passthrough,
		b + "scheduleTrigger":                        scheduleTrigger,
		b + "cron":                                   scheduleTrigger,
		b + "interval":                               scheduleTrigger,
		"@n8n/n8n-nodes-langchain.chatTrigger":       passthrough,
		"@n8n/n8n-nodes-langchain.manualChatTrigger": passthrough,
		// flow
		b + "noOp":                         passthrough,
		b + "executionData":                executionDataNode,
		b + "compression":                  compressionNode,
		b + "readPDF":                      readPDFNode,
		b + "crypto":                       cryptoNode,
		b + "youTube":                      youTubeNode,
		b + "moveBinaryData":               moveBinaryDataNode,
		b + "n8nTrainingCustomerDatastore": trainingCustomerDatastoreNode,
		b + "compareDatasets":              compareDatasetsNode,
		b + "debugHelper":                  debugHelperNode,
		b + "aiTransform":                  aiTransformNode,
		b + "github":                       githubNode,
		b + "hackerNews":                   hackerNewsNode,
		b + "reddit":                       redditNode,
		b + "twilio":                       twilioNode,
		b + "twilioTrigger":                passthrough,
		b + "openWeatherMap":               openWeatherMapNode,
		b + "urlScanIo":                    urlScanIoNode,
		b + "apiTemplateIo":                apiTemplateIoNode,
		b + "uproc":                        uprocNode,
		b + "wooCommerce":                  wooCommerceNode,
		b + "microsoftOneDrive":            microsoftOneDriveNode,
		b + "microsoftExcel":               microsoftExcelNode,
		b + "microsoftTeams":               microsoftTeamsNode,
		b + "microsoftOutlookTrigger":      microsoftOutlookTriggerNode,
		b + "baserow":                      baserowNode,
		b + "nocoDb":                       nocoDbNode,
		b + "odoo":                         odooNode,
		b + "jira":                         jiraNode,
		b + "ghost":                        ghostNode,
		b + "dropbox":                      dropboxNode,
		b + "googleTasks":                  googleTasksNode,
		b + "googleAnalytics":              googleAnalyticsNode,
		b + "s3":                           s3Node,
		b + "mongoDb":                      mongoDbNode,
		b + "rssFeedReadTrigger":           rssFeedReadTriggerNode,
		b + "notionTrigger":                notionTriggerNode,
		b + "googleCalendarTrigger":        googleCalendarTriggerNode,
		b + "airtableTrigger":              airtableTriggerNode,
		b + "dhl":                          dhlNode,
		b + "n8n":                          n8nAPINode,
		b + "executeCommand":               executeCommandNode,
		b + "localFileTrigger":             localFileTriggerNode,
		b + "dataTable":                    dataTableNode,
		b + "hubspot":                      hubspotNode,
		b + "perplexity":                   perplexityNode,
		b + "if":                           ifNode,
		b + "filter":                       filterNode,
		b + "switch":                       switchNode,
		b + "merge":                        mergeNode,
		b + "set":                          setNode,
		b + "splitOut":                     splitOutNode,
		b + "aggregate":                    aggregateNode,
		b + "limit":                        limitNode,
		b + "sort":                         sortNode,
		b + "removeDuplicates":             removeDuplicatesNode,
		b + "wait":                         waitNode,
		b + "respondToWebhook":             respondToWebhookNode,
		b + "stopAndError":                 stopAndErrorNode,
		b + "code":                         codeNode,
		b + "function":                     functionNode,
		b + "functionItem":                 functionItemNode,
	}
}

// Register adds every supported n8n node type to the registry.
func Register(reg *engine.Registry, opts *Options) {
	if opts == nil {
		opts = &Options{}
	}
	for typ, fn := range nodeTypes() {
		reg.Register(typ, &executor{typ: typ, fn: fn, opts: opts})
	}
	for typ, fn := range extraNodeTypes(opts) {
		reg.Register(typ, &executor{typ: typ, fn: fn, opts: opts})
	}
	for typ := range serviceHooks {
		// The webhook handler builds the items; the trigger passes them on.
		reg.Register(typ, &executor{typ: typ, fn: passthrough, opts: opts})
	}
}

// Supported reports whether an n8n node type has an implementation.
func Supported(typ string) bool {
	if _, ok := nodeTypes()[typ]; ok {
		return true
	}
	if _, ok := extraNodeTypes(&Options{})[typ]; ok {
		return true
	}
	if serviceHooks[typ].handle != nil {
		return true
	}
	return aiSubNodeSupported(typ)
}

// extraNodeTypes holds node types that need options (HTTP, loops, AI, …).
func extraNodeTypes(opts *Options) map[string]nodeFunc {
	b, lc := "n8n-nodes-base.", "@n8n/n8n-nodes-langchain."
	return map[string]nodeFunc{
		CycleNodeType:                        cycleNode,
		b + "httpRequest":                    httpRequestNode,
		b + "telegram":                       telegramNode,
		b + "googleSheets":                   googleSheetsNode,
		b + "gmail":                          gmailNode,
		b + "googleDrive":                    googleDriveNode,
		b + "extractFromFile":                extractFromFileNode,
		b + "convertToFile":                  convertToFileNode,
		b + "markdown":                       markdownNode,
		b + "html":                           htmlNode,
		b + "xml":                            xmlNode,
		b + "emailSend":                      emailSendNode,
		b + "gmailTrigger":                   gmailTriggerNode,
		b + "emailReadImap":                  emailReadImapNode,
		b + "googleSheetsTrigger":            googleSheetsTriggerNode,
		b + "googleDriveTrigger":             googleDriveTriggerNode,
		b + "whatsApp":                       whatsAppNode,
		b + "wordpress":                      wordpressNode,
		b + "notion":                         notionNode,
		b + "slack":                          slackNode,
		b + "discord":                        discordNode,
		b + "facebookGraphApi":               facebookGraphNode,
		b + "googleCalendar":                 googleCalendarNode,
		b + "googleDocs":                     googleDocsNode,
		b + "form":                           formNode,
		b + "openAi":                         openAiLegacyNode,
		b + "twitter":                        twitterNode,
		b + "linkedIn":                       linkedInNode,
		b + "postgres":                       postgresNode,
		b + "itemLists":                      itemListsNode,
		b + "airtable":                       airtableNode,
		b + "microsoftOutlook":               outlookNode,
		b + "editImage":                      editImageNode,
		lc + "chainSummarization":            chainSummarizationNode,
		lc + "chainRetrievalQa":              chainRetrievalQaNode,
		b + "summarize":                      summarizeNode,
		b + "dateTime":                       dateTimeNode,
		b + "supabase":                       supabaseNode,
		b + "redis":                          redisNode,
		b + "mySql":                          mysqlNode,
		b + "spreadsheetFile":                spreadsheetFileNode,
		b + "rssFeedRead":                    rssFeedReadNode,
		b + "htmlExtract":                    htmlExtractNode,
		b + "readWriteFile":                  readWriteFileNode,
		b + "readBinaryFile":                 readWriteFileNode,
		b + "writeBinaryFile":                readWriteFileNode,
		lc + "vectorStoreInMemory":           vectorStoreNode,
		lc + "vectorStoreQdrant":             vectorStoreNode,
		lc + "vectorStorePinecone":           vectorStoreNode,
		lc + "vectorStoreSupabase":           vectorStoreNode,
		b + "splitInBatches":                 splitInBatchesNode(opts),
		b + "executeWorkflow":                executeWorkflowNode(opts),
		lc + "agent":                         agentNode,
		lc + "chainLlm":                      chainLlmNode,
		lc + "openAi":                        openAiMessageNode,
		lc + "textClassifier":                textClassifierNode,
		lc + "sentimentAnalysis":             sentimentNode,
		lc + "memoryManager":                 memoryManagerNode,
		lc + "chat":                          chatNode,
		lc + "code":                          langchainCodeNode,
		lc + "mcpTrigger":                    mcpTriggerNode,
		"n8n-nodes-mcp.mcpClient":            mcpCommunityNode,
		"n8n-nodes-mcp.mcpClientTool":        mcpCommunityNode,
		"@tavily/n8n-nodes-tavily.tavily":    tavilyNode,
		"@apify/n8n-nodes-apify.apify":       apifyNode,
		"CUSTOM.scrapeNinja":                 scrapeNinjaNode,
		"n8n-nodes-scrapeninja.scrapeNinja":  scrapeNinjaNode,
		"n8n-nodes-infranodus.infranodus":    infranodusNode,
		"@blotato/n8n-nodes-blotato.blotato": blotatoNode,
		"n8n-nodes-youtube-transcription.youtubeTranscripter": youtubeTranscripterNode,
		lc + "vectorStorePGVector":                            vectorStoreNode,
		lc + "vectorStoreMongoDBAtlas":                        vectorStoreNode,
		lc + "informationExtractor":                           informationExtractorNode,
		lc + "googleGemini":                                   googleGeminiNode,
		lc + "anthropic":                                      anthropicNode,
	}
}

// ChatMemoryStore keeps chat history for AI memory nodes.
type ChatMemoryStore interface {
	Load(ctx context.Context, sessionKey string) ([]ChatMessage, error)
	Append(ctx context.Context, sessionKey string, msgs ...ChatMessage) error
}

// ChatMessage is one stored chat turn.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
