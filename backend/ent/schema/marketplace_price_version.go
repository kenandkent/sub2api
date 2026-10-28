package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

type MarketplacePriceVersion struct{ ent.Schema }

func (MarketplacePriceVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "marketplace_price_versions"}}
}
func (MarketplacePriceVersion) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (MarketplacePriceVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("version").Unique().Positive(),
		field.JSON("rules", map[string]any{}).Default(func() map[string]any { return map[string]any{} }).SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("published_by").Optional().Nillable(), field.Time("published_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
