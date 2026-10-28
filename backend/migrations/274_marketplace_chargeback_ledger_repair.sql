-- Prior chargebacks moved the buyer projection but omitted its ledger leg.
-- Append a correcting, balanced journal; never rewrite the original event.
CREATE TEMP TABLE marketplace_chargebacks_needing_buyer_ledger ON COMMIT DROP AS
 SELECT c.event_id,c.user_id,c.amount
 FROM marketplace_payment_chargebacks c
 WHERE NOT EXISTS (SELECT 1 FROM ledger_transactions t WHERE t.operation_id='payment-chargeback-buyer:'||c.event_id);
INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint)
 SELECT 'payment-chargeback-buyer:'||event_id,'payment_refund','USD','payment-chargeback-buyer:'||event_id
 FROM marketplace_chargebacks_needing_buyer_ledger;
INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount)
 SELECT t.id,x.account,c.user_id,x.side,c.amount
 FROM marketplace_chargebacks_needing_buyer_ledger c
 JOIN ledger_transactions t ON t.operation_id='payment-chargeback-buyer:'||c.event_id
 CROSS JOIN (VALUES ('buyer_available','debit'),('chargeback_recovery','credit')) x(account,side);
UPDATE ledger_transactions t SET sealed=true
 FROM marketplace_chargebacks_needing_buyer_ledger c
 WHERE t.operation_id='payment-chargeback-buyer:'||c.event_id;
