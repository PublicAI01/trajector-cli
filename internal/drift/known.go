package drift

// The known-value lists below are a snapshot of what Claude Code
// 2.1.26x wrote into session files. They only sort a value into "seen
// before" or "new" for logging; a value outside a list is recorded as
// it is, never refused, and the lists are expected to fall behind.

// knownLaunchSurfaces are the values the launch surface field took.
var knownLaunchSurfaces = set(
	"cli", "mcp", "sdk-cli", "sdk-ts", "sdk-py", "bench",
	"claude-vscode", "claude-code-github-action",
	"local-agent", "local_agent",
	"claude-desktop", "claude-desktop-3p",
	"remote", "remote_baku", "remote_cowork", "remote_trigger",
	"remote_cowork_trigger", "remote_desktop", "remote_mobile",
	"claude_in_slack", "claude-in-slack", "claude-in-teams",
	"claude-security", "ssh-remote",
	"claude-coworker", "claude-coworker-terminal",
)

// knownTopLevelTypes are the values of a line's type field: the
// conversation kinds, and the metadata kinds written beside them.
var knownTopLevelTypes = set(
	"user", "assistant", "system", "attachment", "progress",
	"mode", "last-prompt", "permission-mode", "ai-title", "custom-title",
	"tag", "atis-latch", "agent-name", "agent-color", "pr-link",
	"cost-state", "queue-operation",
	"file-history-snapshot", "file-history-delta",
	"relocated", "continued-in", "attribution-snapshot",
	"artifact-autoreact-ledger", "summary", "history-suppression",
	"content-replacement", "bridge-session", "fork-context-ref",
)

// knownSystemSubtypes are the values of subtype on a system line.
var knownSystemSubtypes = set(
	"turn_duration", "away_summary", "local_command", "compact_boundary",
	"stop_hook_summary", "scheduled_task_fire", "model_refusal_fallback",
	"api_error", "informational",
)

// knownAttachmentTypes are the values of attachment.type on an
// attachment line.
var knownAttachmentTypes = set(
	"total_tokens_reminder", "environment", "queued_command",
	"prompt_snapshot", "edited_text_file", "model", "session_context",
	"remote_session_change", "deferred_tools_delta", "date",
	"command_permissions", "agent_listing_delta", "instructions",
	"auto_mode", "skill_listing", "file", "deferred_tools_record",
	"nested_memory", "compact_file_reference", "invoked_skills",
	"hook_system_message", "read_truncation_notice",
	"output_style", "hook_success", "output_style_instructions",
	"bash_output_audience_note", "batching_reminder_sent",
	"silent_turn_reminder",
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
