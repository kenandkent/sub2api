-- Compatibility bridge for existing SQL/Ent balance writers. No user is
-- migrated automatically: the explicit function locks and verifies each user.
CREATE OR REPLACE FUNCTION marketplace_legacy_journal(op text,uid bigint,debit_account text,credit_account text,value numeric) RETURNS void LANGUAGE plpgsql AS $$
DECLARE journal_id bigint;
BEGIN
 IF value=0 THEN RETURN; END IF;
 IF value<0 OR value<>round(value,8) THEN RAISE EXCEPTION 'invalid legacy journal amount'; END IF;
 INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint)
 VALUES(op,'legacy_compat','USD',md5(uid::text||':'||debit_account||':'||credit_account||':'||value::text)) RETURNING id INTO journal_id;
 INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount) VALUES
   (journal_id,debit_account,uid,'debit',value),(journal_id,credit_account,uid,'credit',value);
 UPDATE ledger_transactions SET sealed=true WHERE id=journal_id;
END $$;

CREATE OR REPLACE FUNCTION marketplace_migrate_funding_user(uid bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE b numeric; h numeric; mapped numeric; lot bigint; job record; hold_key text;
BEGIN
 SELECT balance,coalesce(frozen_balance,0) INTO STRICT b,h FROM users WHERE id=uid AND deleted_at IS NULL FOR UPDATE;
 IF EXISTS(SELECT 1 FROM wallet_funding_states WHERE user_id=uid) THEN RETURN; END IF;
 IF EXISTS(SELECT 1 FROM payment_orders WHERE user_id=uid AND status IN ('REFUNDING','REFUND_PENDING')) THEN
   RAISE EXCEPTION 'in-flight payment refund prevents wallet migration for user %',uid;
 END IF;
 SELECT coalesce(sum(round(coalesce(j.hold_amount,j.estimated_cost),8)),0) INTO mapped
 FROM batch_image_jobs j WHERE j.user_id=uid AND j.settled_at IS NULL AND coalesce(j.hold_amount,j.estimated_cost)>0
 AND EXISTS(SELECT 1 FROM (SELECT request_id,api_key_id FROM usage_billing_dedup UNION ALL SELECT request_id,api_key_id FROM usage_billing_dedup_archive) d
   WHERE d.request_id='batch_image_hold:'||j.batch_id AND d.api_key_id=j.api_key_id)
 AND NOT EXISTS(SELECT 1 FROM (SELECT request_id,api_key_id FROM usage_billing_dedup UNION ALL SELECT request_id,api_key_id FROM usage_billing_dedup_archive) d
   WHERE d.request_id IN ('batch_image_capture:'||j.batch_id,'batch_image_release:'||j.batch_id) AND d.api_key_id=j.api_key_id);
 IF mapped<>h THEN RAISE EXCEPTION 'unmapped legacy frozen balance for user %',uid; END IF;
 INSERT INTO wallet_funding_states(user_id,debt,migration_version,reconciled_at) VALUES(uid,greatest(-b,0),1,now());
 IF b>0 THEN PERFORM marketplace_legacy_journal('migration:'||uid||':available',uid,'funding_migration_opening','buyer_available',b);
 ELSIF b<0 THEN PERFORM marketplace_legacy_journal('migration:'||uid||':available',uid,'buyer_available','funding_migration_opening',-b); END IF;
 IF h>0 THEN PERFORM marketplace_legacy_journal('migration:'||uid||':frozen',uid,'funding_migration_opening','buyer_frozen',h); END IF;
 IF greatest(b,0)+h>0 THEN
   INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining,reserved)
   VALUES(uid,'legacy_unverified','migration',uid::text,greatest(b,0)+h,greatest(b,0)+h,h) RETURNING id INTO lot;
 END IF;
 FOR job IN SELECT j.batch_id,round(coalesce(j.hold_amount,j.estimated_cost),8) amount FROM batch_image_jobs j
 WHERE j.user_id=uid AND j.settled_at IS NULL AND coalesce(j.hold_amount,j.estimated_cost)>0
 AND EXISTS(SELECT 1 FROM (SELECT request_id,api_key_id FROM usage_billing_dedup UNION ALL SELECT request_id,api_key_id FROM usage_billing_dedup_archive) d WHERE d.request_id='batch_image_hold:'||j.batch_id AND d.api_key_id=j.api_key_id)
 AND NOT EXISTS(SELECT 1 FROM (SELECT request_id,api_key_id FROM usage_billing_dedup UNION ALL SELECT request_id,api_key_id FROM usage_billing_dedup_archive) d WHERE d.request_id IN ('batch_image_capture:'||j.batch_id,'batch_image_release:'||j.batch_id) AND d.api_key_id=j.api_key_id)
 LOOP
   hold_key:='batch_image_hold:'||job.batch_id;
   INSERT INTO wallet_funding_operations(operation_id,user_id,actor,operation_type,fingerprint) VALUES(hold_key,uid,'migration','legacy_hold',job.amount::text);
   INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved) VALUES(lot,hold_key,job.amount);
 END LOOP;
END $$;

CREATE OR REPLACE FUNCTION marketplace_sync_legacy_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE d numeric; dh numeric; debt_value numeric; total numeric; held numeric; left_value numeric; take_value numeric;
 paid numeric; cost numeric; open_total numeric; release_value numeric; op text; hold_key text; entry_id bigint;
 source_kind text; source_origin text; source_id text; row_value record;
BEGIN
 IF coalesce(current_setting('sub2api.funding_explicit',true),'')='true' THEN RETURN NEW; END IF;
 IF NEW.balance=OLD.balance AND NEW.frozen_balance IS NOT DISTINCT FROM OLD.frozen_balance THEN RETURN NEW; END IF;
 SELECT debt INTO debt_value FROM wallet_funding_states WHERE user_id=NEW.id FOR UPDATE;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT coalesce(sum(remaining),0),coalesce(sum(reserved),0) INTO total,held FROM wallet_funding_lots WHERE user_id=NEW.id;
 IF total-debt_value<>OLD.balance+coalesce(OLD.frozen_balance,0) OR held<>coalesce(OLD.frozen_balance,0) THEN
   RAISE EXCEPTION 'legacy funding projection invariant violated for user %',NEW.id;
 END IF;
 d:=NEW.balance-OLD.balance+coalesce(NEW.frozen_balance,0)-coalesce(OLD.frozen_balance,0);
 dh:=coalesce(NEW.frozen_balance,0)-coalesce(OLD.frozen_balance,0);
 op:='legacy:'||NEW.id||':'||txid_current()||':'||md5(random()::text||clock_timestamp()::text);
 hold_key:=nullif(current_setting('sub2api.legacy_hold_id',true),'');
 IF dh>0 THEN
   IF d<>0 OR hold_key IS NULL THEN RAISE EXCEPTION 'legacy hold requires exact business key'; END IF;
   INSERT INTO wallet_funding_operations(operation_id,user_id,actor,operation_type,fingerprint) VALUES(hold_key,NEW.id,'legacy','legacy_hold',dh::text);
   left_value:=dh;
   FOR row_value IN SELECT id,remaining-reserved available FROM wallet_funding_lots WHERE user_id=NEW.id AND remaining>reserved
     ORDER BY CASE kind WHEN 'promotional' THEN 0 WHEN 'legacy_unverified' THEN 1 WHEN 'platform_subsidy' THEN 2 ELSE 3 END,id FOR UPDATE
   LOOP
     take_value:=least(left_value,row_value.available);
     UPDATE wallet_funding_lots SET reserved=reserved+take_value WHERE id=row_value.id;
     INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved) VALUES(row_value.id,hold_key,take_value);
     left_value:=left_value-take_value;
     EXIT WHEN left_value=0;
   END LOOP;
   IF left_value<>0 THEN RAISE EXCEPTION 'insufficient legacy hold funding'; END IF;
 ELSIF dh<0 THEN
   IF hold_key IS NULL OR d>0 OR -d> -dh THEN RAISE EXCEPTION 'invalid legacy hold settlement'; END IF;
   SELECT coalesce(sum(a.reserved-a.captured-a.released),0) INTO open_total FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE a.hold_operation_id=hold_key AND l.user_id=NEW.id;
   IF open_total<> -dh THEN RAISE EXCEPTION 'legacy hold allocation mismatch'; END IF;
   cost:=-d;
   FOR row_value IN SELECT a.id,a.lot_id,a.reserved-a.captured-a.released open_amount FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE a.hold_operation_id=hold_key AND l.user_id=NEW.id ORDER BY a.lot_id FOR UPDATE OF a,l
   LOOP
     take_value:=least(cost,row_value.open_amount); cost:=cost-take_value;
     release_value:=row_value.open_amount-take_value; paid:=least(release_value,debt_value); debt_value:=debt_value-paid;
     UPDATE wallet_funding_lots SET remaining=remaining-take_value-paid,reserved=reserved-row_value.open_amount WHERE id=row_value.lot_id;
     UPDATE wallet_funding_allocations SET captured=wallet_funding_allocations.captured+take_value,released=wallet_funding_allocations.released+(row_value.open_amount-take_value) WHERE id=row_value.id;
   END LOOP;
 ELSIF d>0 THEN
   paid:=least(d,debt_value); debt_value:=debt_value-paid;
   source_kind:='promotional'; source_origin:='legacy_compat'; source_id:=op;
   -- Cash is classified by a verified payment redeem reference, never an
   -- arbitrary client-supplied wallet kind or a bare administrator adjustment.
   IF nullif(current_setting('sub2api.payment_redeem_code',true),'') IS NOT NULL THEN
     SELECT p.id::text INTO source_id FROM payment_orders p WHERE p.user_id=NEW.id AND p.recharge_code=current_setting('sub2api.payment_redeem_code',true)
       AND p.order_type='balance' AND p.status IN ('PAID','RECHARGING','COMPLETED') AND round(p.amount,8)=d;
     IF FOUND THEN source_kind:='cash_paid'; source_origin:='payment_order'; ELSE source_id:=op; END IF;
   END IF;
   IF d-paid>0 THEN
     INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining)
     VALUES(NEW.id,source_kind,source_origin,source_id,d-paid,d-paid);
   END IF;
 ELSIF d<0 THEN
   left_value:=-d;
   FOR row_value IN SELECT id,remaining-reserved available FROM wallet_funding_lots WHERE user_id=NEW.id AND remaining>reserved
     ORDER BY CASE kind WHEN 'promotional' THEN 0 WHEN 'legacy_unverified' THEN 1 WHEN 'platform_subsidy' THEN 2 ELSE 3 END,id FOR UPDATE
   LOOP
     take_value:=least(left_value,row_value.available);
     UPDATE wallet_funding_lots SET remaining=remaining-take_value WHERE id=row_value.id;
     left_value:=left_value-take_value;
     EXIT WHEN left_value=0;
   END LOOP;
   debt_value:=debt_value+left_value;
 END IF;
 UPDATE wallet_funding_states SET debt=debt_value,reconciled_at=now() WHERE user_id=NEW.id;
 SELECT coalesce(sum(remaining),0),coalesce(sum(reserved),0) INTO total,held FROM wallet_funding_lots WHERE user_id=NEW.id;
 IF total-debt_value<>NEW.balance+coalesce(NEW.frozen_balance,0) OR held<>coalesce(NEW.frozen_balance,0) THEN
   RAISE EXCEPTION 'legacy funding update invariant violated for user %',NEW.id;
 END IF;
 -- Signed wallet accounts make the ledger reconstruct the existing B/H
 -- projections, including a negative available balance from legacy overdraft.
 IF dh>0 THEN
   PERFORM marketplace_legacy_journal(op||':hold',NEW.id,'buyer_available','buyer_frozen',dh);
 ELSIF dh<0 THEN
   PERFORM marketplace_legacy_journal(op||':capture',NEW.id,'buyer_frozen','self_consumption',-d);
   PERFORM marketplace_legacy_journal(op||':release',NEW.id,'buyer_frozen','buyer_available',-dh+d);
 ELSIF d>0 THEN
   PERFORM marketplace_legacy_journal(op||':credit',NEW.id,'funding_source','buyer_available',d);
 ELSIF d<0 THEN
   PERFORM marketplace_legacy_journal(op||':debit',NEW.id,'buyer_available','self_consumption',-d);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER marketplace_legacy_balance_bridge BEFORE UPDATE OF balance,frozen_balance ON users FOR EACH ROW EXECUTE FUNCTION marketplace_sync_legacy_balance();
