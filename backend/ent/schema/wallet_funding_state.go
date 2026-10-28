package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"time"
)

type WalletFundingState struct{ ent.Schema }

func (WalletFundingState) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "wallet_funding_states"}}
}
func (WalletFundingState) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id").Unique(), marketplaceAmount("debt"), field.Int("migration_version").Default(0), field.Time("reconciled_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
