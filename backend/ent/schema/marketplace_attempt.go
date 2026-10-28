package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type MarketplaceAttempt struct{ ent.Schema }

func (MarketplaceAttempt) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "marketplace_attempts"}}
}
func (MarketplaceAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.String("receipt_id").SchemaType(map[string]string{dialect.Postgres: "uuid"}).Immutable(), field.Int("ordinal").Positive().Immutable(),
		field.Int64("account_id").Immutable(), field.Int64("offer_id").Immutable(), field.Int64("owner_user_id").Immutable(),
		field.JSON("quote", map[string]any{}).Immutable(), field.String("quote_digest").NotEmpty().Immutable(),
		field.String("status"), field.JSON("usage", map[string]any{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
func (MarketplaceAttempt) Indexes() []ent.Index {
	return []ent.Index{index.Fields("receipt_id", "ordinal").Unique()}
}
