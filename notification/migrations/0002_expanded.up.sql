-- NT06: a position recipient whose holders were found is recorded as expanded (once: a later run does not expand it
-- again); its key carries the date it was expanded on.
ALTER TABLE delivery DROP CONSTRAINT delivery_result_check;
ALTER TABLE delivery ADD CONSTRAINT delivery_result_check
    CHECK (result IN ('written', 'preference_off', 'expired', 'unresolvable', 'expanded'));
