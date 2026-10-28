package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type SupplierWallet struct{ ent.Schema }

func (SupplierWallet) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "supplier_wallets"}}
}
func (SupplierWallet) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"), field.String("currency").Default("USD"), marketplaceAmount("pending"), marketplaceAmount("available"), marketplaceAmount("payout_reserved"), marketplaceAmount("debt"), field.Int64("version").Default(1),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (SupplierWallet) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "currency").Unique()}
}
