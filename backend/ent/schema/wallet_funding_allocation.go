package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type WalletFundingAllocation struct{ ent.Schema }

func (WalletFundingAllocation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "wallet_funding_allocations"}}
}
func (WalletFundingAllocation) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("lot_id"), field.String("hold_operation_id"), marketplaceAmount("reserved"), marketplaceAmount("captured"), marketplaceAmount("released"), marketplaceAmount("refunded"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (WalletFundingAllocation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("hold_operation_id", "lot_id").Unique()}
}
