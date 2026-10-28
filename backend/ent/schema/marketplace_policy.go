package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

type MarketplacePolicy struct{ ent.Schema }

func (MarketplacePolicy) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "marketplace_policies"}}
}
func (MarketplacePolicy) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (MarketplacePolicy) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("version").Unique().Positive(),
		field.Int("fee_bps").Range(0, 10000), field.Int("hold_hours").Range(0, 87600), field.Int("max_supplier_concurrency").Positive(),
		field.String("minimum_payout_usd").SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}),
		field.JSON("rules", map[string]any{}).Default(func() map[string]any { return map[string]any{} }).SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("published_by").Optional().Nillable(), field.Time("published_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
