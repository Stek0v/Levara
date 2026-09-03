package mcp

func longHorizonToolDescriptors() []Tool {
	obj := func() map[string]any { return map[string]any{"type": "object"} }
	arrObj := func() map[string]any { return map[string]any{"type": "array", "items": obj()} }
	nonEmpty := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	definitionOfDone := map[string]any{
		"type": "array", "minItems": 1, "description": "Required, addressable completion criteria.",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"criterion_id": nonEmpty("Stable ID used by task_receipt and task_plan."),
			"description":  nonEmpty("Verifiable completion requirement."),
			"required":     booleanProp("Defaults to true."),
			"verification": obj(),
		}, "required": []string{"criterion_id", "description"}},
	}
	validationSchema := objectSchema(map[string]any{
		"valid": booleanProp("True when all completion invariants hold."), "task_id": stringProp("Task ID."),
		"mode": stringProp("checkpoint or completion."), "version": integerProp("Current task version."),
		"missing_receipts": arrayOfStringsProp("Criteria without evidence."), "stale_receipts": arrayOfStringsProp("Criteria backed only by stale evidence."),
		"failed_receipts": arrayOfStringsProp("Criteria backed only by failed evidence."), "incomplete_steps": arrayOfStringsProp("Required steps not passed."),
		"active_blockers": arrayOfStringsProp("Active blocker IDs."), "active_leases": arrayOfStringsProp("Active leased step IDs."),
		"audit_required": booleanProp("Whether risk policy requires reviewer evidence."), "reviewer_satisfied": booleanProp("Whether a current passing reviewer receipt exists."),
	})
	return []Tool{
		{Name: "supersede_memory", Group: "memory", Description: "Replace an active memory while preserving the old row, provenance, reason, and vector consistency.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when replaced."), "old_memory_id": stringProp("Superseded row."), "new_memory_id": stringProp("Replacement row."), "key": stringProp("Stable active key."), "reason": stringProp("Supersession reason.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"old_memory_id": nonEmpty("Active memory ID."), "new_value": nonEmpty("Replacement value."), "reason": nonEmpty("Why the old value is obsolete."), "key": stringProp("Optional replacement key."), "room": stringProp("Optional replacement room."), "hall": stringProp("Optional replacement hall."), "source_task_id": stringProp("Origin task."), "source_receipt_ids": arrayOfStringsProp("Evidence receipt IDs."), "verification_status": stringProp("Verification state.")}, "required": []string{"old_memory_id", "new_value", "reason"}}},
		{Name: "task_open", Group: "task", Description: "Create or idempotently reopen a versioned long-horizon task scoped to one collection and room.",
			OutputSchema: objectSchema(map[string]any{"task_id": stringProp("Task ID."), "status": stringProp("Task state."), "version": integerProp("Optimistic version."), "collection": stringProp("Collection."), "room": stringProp("Room.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"collection": nonEmpty("Non-empty collection."), "room": nonEmpty("Non-empty task subsystem."), "objective": nonEmpty("Verifiable outcome."), "idempotency_key": nonEmpty("Stable create key."), "risk_level": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}}, "authority": obj(), "definition_of_done": definitionOfDone, "actor_id": stringProp("Agent identity.")}, "required": []string{"collection", "room", "objective", "idempotency_key", "definition_of_done"}}},
		{Name: "task_bootstrap", Group: "task", Description: "Return a bounded recovery snapshot with task state, next step, blockers, and strictly scoped durable memories.",
			OutputSchema: objectSchema(map[string]any{"task_id": stringProp("Task ID."), "collection": stringProp("Collection."), "room": stringProp("Room."), "objective": stringProp("Outcome."), "risk_level": stringProp("Risk."), "status": stringProp("State."), "version": integerProp("Version."), "workspace_revision": stringProp("Current workspace revision."), "criteria": arrObj(), "steps": arrObj(), "active_blockers": arrObj(), "last_checkpoint": arrObj(), "next_step": obj(), "memories": arrObj(), "scope_status": stringProp("exact."), "max_tokens": integerProp("Budget."), "tokens_used": integerProp("Approximate tokens.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "max_tokens": integerProp("100-4000 token budget.")}, "required": []string{"task_id"}}},
		{Name: "task_plan", Group: "task", Description: "Replace a not-yet-started task plan using optimistic concurrency and validated dependencies.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when saved."), "task_id": stringProp("Task ID."), "status": stringProp("planned."), "version": integerProp("New version."), "steps": integerProp("Step count.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "base_version": integerProp("Expected version."), "steps": arrObj(), "actor_id": stringProp("Agent identity.")}, "required": []string{"task_id", "base_version", "steps"}}},
		{Name: "task_step", Group: "task", Description: "Atomically claim, renew, release, pass, or fail a task step lease.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when transitioned."), "task_id": stringProp("Task ID."), "step_id": stringProp("Step ID."), "action": stringProp("Applied action."), "version": integerProp("New version.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "step_id": nonEmpty("Step ID."), "action": map[string]any{"type": "string", "enum": []string{"claim", "renew", "release", "pass", "fail"}}, "base_version": integerProp("Expected task version."), "actor_id": stringProp("Lease owner."), "lease_seconds": integerProp("30-3600 seconds.")}, "required": []string{"task_id", "step_id", "action", "base_version"}}},
		{Name: "task_checkpoint", Group: "task", Description: "Persist compact recovery state, blockers, and durable-memory candidates under optimistic concurrency.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when saved."), "task_id": stringProp("Task ID."), "checkpoint_id": stringProp("Checkpoint ID."), "status": stringProp("running or blocked."), "version": integerProp("New version."), "resolved_blockers": integerProp("Blockers resolved by this checkpoint."), "idempotent_replay": booleanProp("True when an existing checkpoint was returned.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "idempotency_key": nonEmpty("Stable checkpoint key."), "base_version": integerProp("Expected version."), "step_id": stringProp("Current step."), "summary": nonEmpty("Verified compact summary."), "verified": arrayOfStringsProp("Verified items."), "failed": arrayOfStringsProp("Failed items."), "next_action": stringProp("Next action."), "workspace_revision": stringProp("Current revision."), "blocker": obj(), "resolved_blocker_ids": arrayOfStringsProp("Active blocker IDs resolved by this checkpoint."), "memory_candidates": arrObj(), "actor_id": stringProp("Agent identity.")}, "required": []string{"task_id", "idempotency_key", "base_version", "summary"}}},
		{Name: "task_receipt", Group: "task", Description: "Record immutable command, artifact, source, observation, or reviewer evidence for Definition of Done criteria.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when recorded."), "task_id": stringProp("Task ID."), "receipt_id": stringProp("Receipt ID."), "version": integerProp("New version."), "idempotent_replay": booleanProp("True when an existing receipt was returned.")}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "idempotency_key": nonEmpty("Stable receipt key."), "base_version": integerProp("Expected version."), "receipt_type": map[string]any{"type": "string", "enum": []string{"command", "artifact", "source", "observation", "reviewer"}}, "status": map[string]any{"type": "string", "enum": []string{"pass", "fail"}}, "criterion_ids": arrayOfStringsProp("DoD criterion IDs."), "observation": stringProp("Observed result."), "exit_code": integerProp("Command exit code."), "evidence_uri": stringProp("External artifact URI."), "artifact_digest": stringProp("SHA-256 or equivalent."), "workspace_revision": stringProp("Workspace revision."), "metadata": obj(), "actor_id": stringProp("Agent identity.")}, "required": []string{"task_id", "idempotency_key", "base_version", "receipt_type", "status", "criterion_ids"}}},
		{Name: "task_validate", Group: "task", Description: "Deterministically evaluate receipts, freshness, steps, blockers, leases, and risk-based reviewer policy.", OutputSchema: validationSchema,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "mode": map[string]any{"type": "string", "enum": []string{"checkpoint", "completion"}}}, "required": []string{"task_id"}}},
		{Name: "task_complete", Group: "task", Description: "Atomically complete a valid task and promote verified durable-memory candidates.",
			OutputSchema: objectSchema(map[string]any{"ok": booleanProp("True when completed."), "task_id": stringProp("Task ID."), "status": stringProp("completed."), "version": integerProp("New version."), "promoted_memories": integerProp("Promoted candidates."), "rejected_memories": integerProp("Rejected candidates."), "validation": validationSchema}),
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"task_id": nonEmpty("Task ID."), "expected_version": integerProp("Expected version."), "actor_id": stringProp("Agent identity.")}, "required": []string{"task_id", "expected_version"}}},
	}
}
