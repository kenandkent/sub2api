package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/shopspring/decimal"
	"time"
)

type WalletFundingLot struct{ ent.Schema }

func (WalletFundingLot) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "wallet_funding_lots"}}
}
func (WalletFundingLot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"), field.String("kind"), field.String("origin_type"), field.String("origin_id"), field.String("currency").Default("USD"),
		marketplaceAmount("original"), marketplaceAmount("remaining"), marketplaceAmount("reserved"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (WalletFundingLot) Indexes() []ent.Index {
	return []ent.Index{index.Fields("origin_type", "origin_id").Unique(), index.Fields("user_id", "id")}
}
func marketplaceAmount(name string) ent.Field {
	return field.Other(name, decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)", dialect.SQLite: "text"})
}
