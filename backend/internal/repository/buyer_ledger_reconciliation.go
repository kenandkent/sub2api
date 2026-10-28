package repository

// BuyerLedgerDifferencesSQL reports projections that cannot be reconstructed
// from sealed journal entries. Column order: user ID, absolute discrepancy,
// and user creation time. The caller may use it in reconciliation queries.
const BuyerLedgerDifferencesSQL = `SELECT u.id AS user_id,
 (abs(u.balance-coalesce(l.available,0))+abs(u.frozen_balance-coalesce(l.frozen,0))) AS difference,
 u.created_at
 FROM users u JOIN wallet_funding_states f ON f.user_id=u.id
 LEFT JOIN LATERAL (
 SELECT sum(CASE WHEN e.account='buyer_available' THEN CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END ELSE 0 END) AS available,
        sum(CASE WHEN e.account='buyer_frozen' THEN CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END ELSE 0 END) AS frozen
 FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id AND t.sealed
 WHERE e.subject_id=u.id AND e.account IN ('buyer_available','buyer_frozen')
 ) l ON true
 WHERE u.balance<>coalesce(l.available,0) OR u.frozen_balance<>coalesce(l.frozen,0)`
