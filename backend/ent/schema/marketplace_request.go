package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"time"
)

type MarketplaceRequest struct{ ent.Schema }

func (MarketplaceRequest) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "marketplace_requests"}}
}
func (MarketplaceRequest) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").SchemaType(map[string]string{dialect.Postgres: "uuid"}),
		field.Int64("buyer_user_id").Immutable(), field.Int64("api_key_id").Immutable(), field.Int64("group_id").Immutable(),
		field.String("request_digest").NotEmpty().Immutable(), field.String("idempotency_key").Optional().Nillable().Immutable(),
		field.String("status").Default("prepared"),
		field.String("budget").SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Default("0"),
		field.String("actual_charge").SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Default("0"),
		field.Int64("billable_attempt_id").Optional().Nillable(), field.String("review_reason").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
