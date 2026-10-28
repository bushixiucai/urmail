-- No reviewed proof/result/history is deleted. A binary that understands only
-- 0142 cannot verify the canonical digest containing message_guid and refuses
-- those proofs; it must never fall back to a broader destructive selector.
DROP TRIGGER recall_message_identity_guard ON recall_requests;
DROP FUNCTION urmail_recall_message_identity_guard();
