import { page, group } from '../scripts/docs.mjs'

// Curated reading order. Add every new document here: docs.test.mjs fails otherwise.
export const sidebar = [
  page('README', 'Documentation home'),
  group('Start here', [
    page('guides/installation'),
    page('tutorials/first-port'),
    page('tutorials/first-agent'),
    page('guides/web-app'),
  ], false),
  group('Concepts', [
    page('explanation/concepts'),
    page('explanation/message-lifecycle'),
    page('explanation/security-model'),
  ], false),
  group('Guides', [
    group('Channels', [
      page('guides/whatsapp'),
      page('guides/github'),
      page('guides/jira'),
      page('guides/api-docks'),
      page('guides/customs'),
      page('guides/messages-and-images'),
    ], false),
    group('Agents', [
      page('guides/agents-and-routing'),
      page('guides/profiles'),
      page('guides/connector'),
    ], false),
    group('Hosted agents', [
      page('guides/hosting/hosted-agents'),
      page('guides/hosting/network'),
      page('guides/hosting/harness-homes'),
      page('guides/hosting/claude-subscriptions'),
    ]),
    group('Operations', [
      page('guides/operations/deploy'),
      page('guides/operations/iam-mode'),
      page('guides/operations/several-processes'),
      page('guides/operations/backups-and-upgrades'),
    ]),
  ], false),
  group('Reference', [
    page('reference/configuration'),
    page('reference/cli'),
    page('reference/http-api'),
    page('reference/events'),
    page('reference/mcp-tools'),
    page('reference/crew-protocol'),
    page('reference/environment-spec'),
    page('reference/agent-profile'),
    page('reference/permissions'),
    page('reference/limits'),
  ]),
  group('Project', [
    page('architecture', 'Architecture'),
    page('project/known-limitations'),
    page('contributing/README', 'Contributing'),
    page('contributing/documentation'),
  ]),
]
