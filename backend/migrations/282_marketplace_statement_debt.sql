-- Debt belongs to the same immutable ledger cutoff as each statement.
ALTER TABLE supplier_statements ADD COLUMN opening_debt numeric(20,8) NOT NULL DEFAULT 0;
ALTER TABLE supplier_statements ADD COLUMN closing_debt numeric(20,8) NOT NULL DEFAULT 0;
ALTER TABLE supplier_statements ADD COLUMN debt_offset numeric(20,8) NOT NULL DEFAULT 0;
ALTER TABLE supplier_statement_entries ADD COLUMN supplier_debt_offset numeric(20,8) NOT NULL DEFAULT 0;

-- Earlier prototype statements remain tied to their original cutoff and booking
-- month; do not use the current wallet balance or a later capture/refund.
ALTER TABLE supplier_statements DISABLE TRIGGER supplier_statement_immutable;
UPDATE supplier_statements s SET
 opening_debt = COALESCE((SELECT sum(CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END)
   FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id
   WHERE e.subject_id=s.supplier_id AND e.account='supplier_debt' AND t.sealed
     AND t.id<=s.cutoff_ledger_id AND t.booked_at < (s.month::timestamp AT TIME ZONE 'UTC')),0),
 closing_debt = COALESCE((SELECT sum(CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END)
   FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id
   WHERE e.subject_id=s.supplier_id AND e.account='supplier_debt' AND t.sealed
     AND t.id<=s.cutoff_ledger_id AND t.booked_at < ((s.month + interval '1 month') AT TIME ZONE 'UTC')),0),
 debt_offset = COALESCE((SELECT sum(c.supplier_debt_offset) FROM marketplace_captures c
   JOIN ledger_transactions t ON t.id=c.ledger_transaction_id
   WHERE c.owner_user_id=s.supplier_id AND t.id<=s.cutoff_ledger_id AND t.sealed
     AND t.booked_at >= (s.month::timestamp AT TIME ZONE 'UTC')
     AND t.booked_at < ((s.month + interval '1 month') AT TIME ZONE 'UTC')),0);
ALTER TABLE supplier_statements ENABLE TRIGGER supplier_statement_immutable;

ALTER TABLE supplier_statement_entries DISABLE TRIGGER supplier_statement_entries_immutable;
UPDATE supplier_statement_entries e SET supplier_debt_offset=c.supplier_debt_offset
 FROM supplier_statements s, marketplace_captures c, ledger_transactions t
 WHERE e.statement_id=s.id AND e.kind='capture' AND c.owner_user_id=s.supplier_id
   AND t.id=c.ledger_transaction_id AND t.id<=s.cutoff_ledger_id AND t.operation_id=e.operation_id;
ALTER TABLE supplier_statement_entries ENABLE TRIGGER supplier_statement_entries_immutable;
