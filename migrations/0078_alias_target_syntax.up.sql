-- Enforce the canonical alias target grammar at the storage boundary so a
-- rule the API validators miss can never poison the global Postfix render
-- (audit finding MAIL-01).
--
-- NOT VALID keeps pre-existing rows untouched: legacy imports may carry
-- targets this constraint would reject, and validating them must be a
-- deliberate maintenance-window step after review/cleanup, not a silent
-- upgrade failure. Run
--   ALTER TABLE mailbox_aliases VALIDATE CONSTRAINT alias_target_email_syntax;
-- once the data has been confirmed clean.
ALTER TABLE mailbox_aliases ADD CONSTRAINT alias_target_email_syntax CHECK (
    cardinality(string_to_array(target, '@')) = 2
    AND length(split_part(target, '@', 1)) BETWEEN 1 AND 64
    AND strpos(target, '*') = 0
    AND strpos(target, '?') = 0
    AND strpos(target, '/') = 0
    AND strpos(target, '\') = 0
    AND target = lower(target)
) NOT VALID;
