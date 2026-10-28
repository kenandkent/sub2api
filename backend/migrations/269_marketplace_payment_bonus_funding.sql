-- Split verified recharge credits into their order cash and bonus sources.
CREATE OR REPLACE FUNCTION marketplace_sync_legacy_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE d numeric; dh numeric; debt_value numeric; total numeric; held numeric; left_value numeric; take_value numeric;
 paid numeric; cost numeric; open_total numeric; release_value numeric; op text; hold_key text; entry_id bigint;
 source_kind text; source_origin text; source_id text; bonus numeric; cash numeric; cash_paid_debt numeric; row_value record;
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
   -- The verified redeem identifies the entire order credit; only its non-bonus
   -- portion is eligible for shared spending. Debt consumes cash first.
   IF nullif(current_setting('sub2api.payment_redeem_code',true),'') IS NOT NULL THEN
     SELECT p.id::text,round(p.bonus_amount,8) INTO source_id,bonus FROM payment_orders p
       WHERE p.user_id=NEW.id AND p.recharge_code=current_setting('sub2api.payment_redeem_code',true)
       AND p.order_type='balance' AND p.status IN ('PAID','RECHARGING','COMPLETED') AND round(p.amount,8)=d
       AND p.bonus_amount>=0 AND round(p.bonus_amount,8)<=d;
     IF FOUND THEN source_kind:='cash_paid'; source_origin:='payment_order'; ELSE source_id:=op; END IF;
   END IF;
   IF source_kind='cash_paid' THEN
     cash:=d-bonus;
     cash_paid_debt:=least(cash,paid);
     -- Keep the zero cash lot as the order identity even if debt absorbed it.
     INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining)
     VALUES(NEW.id,'cash_paid','payment_order',source_id,cash-cash_paid_debt,cash-cash_paid_debt);
     IF bonus-(paid-cash_paid_debt)>0 THEN
       INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining)
       VALUES(NEW.id,'promotional','payment_bonus',source_id,bonus-(paid-cash_paid_debt),bonus-(paid-cash_paid_debt));
     END IF;
   ELSIF d-paid>0 THEN
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
