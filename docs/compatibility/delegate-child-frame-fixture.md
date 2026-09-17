# Delegate Child Frame Integration Fixture

This test-only helper creates a deterministic delegation lifecycle without an
LLM, external provider, production seed endpoint, or persistent user data. It
uses the real workspace SQLite store and the real runtime HTTP handler.

Run from the Synon Go repository:

```bash
go run ./scripts/compat/delegate-fixture --listen 127.0.0.1:39010
```

The first stdout line is JSON containing `baseUrl`, the temporary database
path, and all fixture IDs. The temporary database is deleted when the process
receives Ctrl+C or SIGTERM. Pass `--database /absolute/test/path/workspace.db`
only when a persistent test database is intentionally needed.

Stable contract:

| Field | Value |
| --- | --- |
| user | `local` |
| project | `delegate-fixture-project` |
| parent frame | `delegate-fixture-parent` |
| child frame | `delegate-fixture-child` |
| parallel child frame | `delegate-fixture-child-parallel` |
| grandchild frame | `delegate-fixture-grandchild` |
| parallel child tool use | `toolu_delegate_fixture_002` |
| grandchild tool use | `toolu_delegate_fixture_grandchild_001` |
| parallel child notification tool use | `toolu_delegate_fixture_notifications_002` |
| parallel notification tool-use message | `delegate-parent-notification-tool-use-002` |
| parallel notification tool-result message | `delegate-parent-notification-tool-result-002` |
| parallel delegate | `evidence-check` |
| grandchild delegate | `citation-audit` |
| tool use | `toolu_delegate_fixture_001` |
| notification tool use | `toolu_delegate_fixture_notifications_001` |
| notification tool-use message | `delegate-parent-notification-tool-use-001` |
| notification tool-result message | `delegate-parent-notification-tool-result-001` |
| delegate | `literature-review` |
| trace | `/api/frames/delegate-fixture-parent/trace-shallow?include_messages=true` |

The trace is a real recursive hierarchy:

```text
root: delegate-fixture-parent (running, 9 messages)
child 1: delegate-fixture-child (completed, 4 messages)
  grandchild: delegate-fixture-grandchild (failed, 2 messages)
child 2: delegate-fixture-child-parallel (needs_input, 2 messages)
```

The parent keeps the original delegate and notification messages, then adds a
second delegate `tool_use`/`tool_result` and a separate notification pair for
the parallel child's question. Its `_tool_id_to_frame_id` maps each tool ID
to the correct child. The first child maps its grandchild tool ID through its
own `_tool_id_to_frame_id`. Every non-root frame has a non-empty
`parent_frame_id`, `delegate_name`, and `_latest_tool_block`.

The notification result block stores `content` as a JSON string, matching the
v1.1 conversation parser. Its exact payload is:

```json
{
  "notifications": [
    {
      "notification_type": "completion",
      "sender_frame_id": "delegate-fixture-child",
      "payload": {
        "status": "completed",
        "_completion_bullets": [
          "Reviewed deterministic fixture evidence.",
          "Returned two source-backed findings."
        ],
        "wall_s": 4.25
      }
    },
    {
      "notification_type": "child_message",
      "sender_frame_id": "delegate-fixture-child",
      "payload": {
        "sender_frame_id": "delegate-fixture-child",
        "name": "literature-review",
        "agent_name": "LITERATURE_REVIEW",
        "kind": "question",
        "text": "Should the review include adjacent therapeutic targets?"
      }
    },
    {
      "notification_type": "child_message",
      "sender_frame_id": "delegate-fixture-child",
      "payload": {
        "sender_frame_id": "delegate-fixture-child",
        "name": "literature-review",
        "agent_name": "LITERATURE_REVIEW",
        "kind": "info",
        "text": "The deterministic evidence set contains two retained sources."
      }
    }
  ]
}
```

The parallel child's needs-input notification is stored in a separate tool
result so the original three-notification payload remains byte-shape
compatible:

```json
{
  "notifications": [
    {
      "notification_type": "child_message",
      "sender_frame_id": "delegate-fixture-child-parallel",
      "payload": {
        "sender_frame_id": "delegate-fixture-child-parallel",
        "name": "evidence-check",
        "agent_name": "EVIDENCE_CHECK",
        "kind": "question",
        "text": "Which assay endpoint should be treated as the primary evidence column?"
      }
    }
  ]
}
```

`message_count` is never seeded independently. The real trace query derives
9, 4, 2, and 2 from each frame's stored `frame_events`. The root trace must
return both direct children and the nested grandchild.

Manual API verification after starting the helper:

```bash
curl -fsS \
  -H 'X-Synon-User-Id: local' \
  'http://127.0.0.1:39010/api/frames/delegate-fixture-parent/trace-shallow?include_messages=true' \
  | jq '{id,status,message_count,children}'
```

Focused verification:

```bash
# SQLite-only test remains runnable while unrelated server work is in flight.
go test internal/testsupport/delegatefixture/fixture.go \
  internal/testsupport/delegatefixture/fixture_store_test.go -count=1

# Includes real HTTP handler and a real WSL child-process lifecycle test.
go test ./internal/testsupport/delegatefixture \
  ./scripts/compat/delegate-fixture -count=1
```
