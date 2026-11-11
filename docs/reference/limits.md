# Limits

Every size, count and time limit built into Seaport, with its Go constant and whether you can change it.

Most limits are fixed in code. The few you can change are environment variables of `seaport serve`; they are marked in the last column and described in [Configuration](configuration.md). Constants are named `package.Name`, relative to `internal/`. A limit that is refused answers `400` (a bad field), `413`, or `422 QUOTA_EXCEEDED` for quotas (see [HTTP API](http-api.md#errors)).

## Per-organization quotas

Counted per organization. `0` turns a configurable quota off.

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Docks | 10 | `config.Server.MaxDocks` | `SEAPORT_MAX_DOCKS` |
| Agents connected at once (all processes) | 20 | `config.Server.MaxCrewConnections` | `SEAPORT_MAX_CREW_CONNECTIONS` |
| Hosted-agent slots | 10 | `config.Server.MaxSlots` | `SEAPORT_MAX_SLOTS` |
| Outbound messages per organization per hour | 1000 | `config.Server.SendOrgPerHour` | `SEAPORT_SEND_ORG_PER_HOUR` |
| Active crew tokens | 100 | `crew.MaxTokens` | No |
| Agents | 100 | `agent.MaxAgents` | No |
| Agent secrets per agent | 50 | `agent.MaxSecrets` | No |
| Environments | 50 | `hosting.MaxEnvironments` | No |
| Harness homes | 500 | `hosting.MaxHomes` | No |
| Slots per hosted agent | 10 | `hosting.MaxSlotsPerAgent` | No |

Slot tokens (the ones a running slot uses) aren't counted against `crew.MaxTokens`.

## Outbound rate limits

Replies over a limit are refused with `429` and `Retry-After`. See [Configuration](configuration.md#outbound-limits).

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Messages per dock per minute | 20 | `config.Server.SendPerMinute` | `SEAPORT_SEND_PER_MINUTE` |
| Messages per dock per hour | 300 | `config.Server.SendPerHour` | `SEAPORT_SEND_PER_HOUR` |
| Messages per conversation per minute | 8 | `config.Server.SendPerChatMinute` | `SEAPORT_SEND_PER_CHAT_MINUTE` |
| Minimum gap between two WhatsApp sends of one dock | 1.2 s | `dockwa.minSendGap` | No |

## Jira docks

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Webhook body | 5 MiB | `dockjira.MaxWebhook` | No |
| Markdown of one comment (longer replies are split) | 24000 bytes | `dockjira.MaxComment` | No |
| Jira API call | 30 s, no redirects, answers read up to 1 MiB | `dockjira` | No |

## API requests

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Requests per second per caller | 20 | `config.Server.APIRate` | `SEAPORT_API_RATE` |
| Burst per caller | 60 | `config.Server.APIBurst` | `SEAPORT_API_BURST` |
| JSON request body | 1 MiB | `httpx.MaxJSONBody` | No |
| Default page size | 20 | `query.DefaultLimit` | No |
| Maximum page size | 100 | `query.MaxLimit` | No |
| Maximum `offset` | 10000 | `query.MaxOffset` | No |
| Model-usage window (`days`) | 366 | `hosting.MaxUsageDays` | No |
| Slot log lines | 2000 | `hosting.MaxLogLines` | No |
| Request header read time | 10 s | `http.Server.ReadHeaderTimeout` in `bootstrap` | No |
| Idle keep-alive connection | 120 s | `http.Server.IdleTimeout` in `bootstrap` | No |

## Sessions and sign-in

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Browser session (admin cookie) lifetime | 7 days | `config.SessionTTL` | No |
| Pending OAuth sign-in (`iam`) | 10 min | `auth.PendingTTL` | No |
| `next` redirect path of a sign-in | 2048 characters | `auth.MaxNext` | No |
| Crew token lifetime | 5 years | `crew.MaxTokenLifetime` | No |
| Slot token lifetime | 30 days | `hosting.SlotTokenLifetime` | No |

## Messages and media

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Message text | 65536 characters | `text.MaxMessage` | No |
| Names (agent label, creator…) | 200 characters | `text.MaxName` | No |
| Search text | 200 characters | `text.MaxSearch` | No |
| Chat identifier | 256 characters | `conversation.MaxExternalID` | No |
| Sender identifier | 256 characters | `ship.MaxSenderID` | No |
| Handles per arrival (chat or sender) | 16 | `conversation.MaxHandles` | No |
| Dock address | 256 characters | `dock.MaxAddress` | No |
| Image size | 3.75 MiB | `ship.MaxImage` | No |
| Attachments per message | 10 | `ship.MaxAttachments` | No |
| Attachment name | 200 characters | `ship.MaxAttachmentName` | No |
| Images in one prompt (connector) | 8 | `connector.maxPromptImages` | No |
| Messages per history page | 100 | `ship.MaxHistory` | No |
| Held messages released when a chat is allowed | newest 50 | `ship.MaxRelease` | No |
| Age of held messages that get released | 24 h | `ship.ReleaseWindow` | No |
| Rules per dock | 1000 | `conversation.MaxRules` | No |
| Rule pattern | 256 characters | `conversation.MaxPattern` | No |

Image storage is on unless `SEAPORT_BLOB_URL=off`. Images of other types are replaced by a note in the text.

## Delivery and crew

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Claim lease of a delivery | 10 min | `config.ClaimLease` | No |
| Expired-claim sweep | every 30 s | `config.SweepInterval` | No |
| Default `concurrency` when a hello omits it | 4 | `crew.DefaultConcurrency` | No |
| Default of `seaport connect --concurrency` | 2 | flag default in `cmd/seaport` | `--concurrency` |
| Maximum `concurrency` | 32 | `crew.MaxConcurrency` | No |
| WebSocket frame | 4 MiB | `crew.MaxFrame` | No |
| Items in one `activity` frame | 32 | `crew.MaxActivityItems` | No |
| Encoded items of one `activity` frame | 6000 bytes | `crew.MaxActivityBytes` | No |
| Live-only event (`crew.activity`) payload | 7000 bytes | `event.MaxFlash` | No |
| Tool calls kept in a turn summary | 200 (the rest counted in `omitted`) | `ship.MaxTurnTools` | No |
| Tool title in a turn summary | 300 characters | `ship.MaxToolTitle` | No |
| WebSocket ping interval (both sides) | 30 s | `crew.PingInterval` | No |
| Longest pause between touches of a process's connections | 10 s (otherwise lease/4) | `crewws.maxTouch` | No |
| One connection operation | 15 s | `crewws.opTimeout` | No |

## Events

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Event retention (resume window) | 1 hour | `config.EventRetention` | No |
| Events replayed or buffered per subscriber | 1000 | `event.maxReplay` | No |
| Heartbeat comment on a stream | 25 s | `eventsse.heartbeat` | No |
| Longest life of one stream | 10 min | `eventsse.maxStream` | No |
| Time a publish may wait | 5 s | `event.publishTimeout` | No |

## Docks across processes

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Dock lease (exclusive channels) | 30 s | `config.DockLease` | No |
| Unclaimed pairing before a linking dock is failed | 1 min | `docksvc.orphanGrace` | No |
| Longest restore backoff of a dock | 5 min | `docksvc.maxBackoff` | No |
| Longest pause after a replaced WhatsApp stream | 10 min | `dockwa.maxReplacedPause` | No |
| WhatsApp pairing wait | 3 min | `dockwa.linkTimeout` | No |
| WhatsApp message handler | 30 s | `dockwa.handlerTimeout` | No |
| WhatsApp media download | 20 s | `dockwa.downloadTimeout` | No |

## Agent profiles

Profile fields are described in [Agent profile](agent-profile.md).

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Description | 1000 characters | `agent.MaxDescription` | No |
| Instructions | 32768 bytes | `agent.MaxInstructions` | No |
| Skills | 50 | `agent.MaxSkills` | No |
| Skill body | 65536 bytes | `agent.MaxSkillBody` | No |
| Skill description | 1024 characters | `agent.maxSkillDescribe` | No |
| MCP servers | 20 | `agent.MaxMCPServers` | No |
| Headers per MCP server | 20 | `agent.MaxHeaders` | No |
| Header value | 4096 characters | `agent.MaxHeaderValue` | No |
| Secret value | 16384 bytes | `agent.MaxSecretValue` | No |
| Model and effort values | 100 characters | `agent.MaxOptionValue` | No |

## Environments and homes

Environment fields are described in [Environment spec](environment-spec.md).

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Environment description | 1000 characters | `hosting.MaxDescription` | No |
| Packages | 100 | `hosting.MaxPackages` | No |
| Setup script | 16384 bytes | `hosting.MaxSetup` | No |
| Repos | 10 | `hosting.MaxRepos` | No |
| Repo `on_start` | 4096 bytes | `hosting.MaxOnStart` | No |
| Egress hosts | 100 | `hosting.MaxEgress` | No |
| Longest `idle_stop` | 7 days | `hosting.MaxIdleStop` | No |
| CPUs per slot | 4 | `config.Server.HostMaxCPUs` | `SEAPORT_HOST_MAX_CPUS` |
| Memory per slot | 8192 MB | `config.Server.HostMaxMemoryMB` | `SEAPORT_HOST_MAX_MEMORY_MB` |
| Home files | 2000 | `hosting.MaxHomeFiles` | No |
| One home file | 5 MiB | `hosting.MaxHomeFile` | No |
| Home total size | 20 MiB | `hosting.MaxHomeBytes` | No |
| Home upload (tar, maybe gzipped) | 25 MiB | `hosting.MaxHomeUpload` | No |
| Home path length | 255 characters | `hosting.MaxHomePath` | No |
| Home path depth | 12 | `hosting.MaxHomeDepth` | No |
| Unused home kept for | 24 h | `hosting.HomeGrace` | No |
| Homes deleted per sweep | 100 | `hosting.MaxHomeSweep` | No |
| Blobs deleted per trash sweep | 100 | `ship.MaxTrashSweep` | No |
| Staged blob kept before deletion | 1 h | `ship.TrashGrace` | No |

`0` for the two `SEAPORT_HOST_MAX_*` values means no limit.

## Builds, slots and sign-in

| Limit | Value | Constant | Configurable |
| :--- | :--- | :--- | :--- |
| Build log kept (its tail) | 256 KiB | `hosting.MaxBuildLog` | No |
| Build with no progress is taken over after | 1 min | `hosting.BuildStale` | No |
| Slot start with no progress is failed after | 2 min | `hosting.StartStale` | No |
| Time an agent gets to finish on stop | 20 s | `hosting.StopTimeout` | No |
| Supervisor period | 15 s | `hosting.SupervisePeriod` | No |
| Egress allowlist cache | 30 s | `hosting.EgressCacheFor` | No |
| Recorded denial per slot and host | once a minute | `hosting.DenialEvery` | No |
| Denied hosts listed per slot | 50 | `hosting.MaxDenials` | No |
| Subscription sign-in duration | 10 min | `hosting.LoginTimeout` | No |
| Sign-in with no beat reads as failed after | 30 s | `hosting.LoginStale` | No |
| Sign-in beat | 5 s | `hosting.LoginBeat` | No |
| Sign-in code | 2048 characters | `hosting.MaxLoginCode` | No |
| Gateway request body | 64 MiB | `hostinggateway.maxRequest` | No |
| Gateway answer read for usage | 16 MiB | `hostinggateway.maxMetered` | No |
