package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

type SupplierProfile struct{ ent.Schema }

func (SupplierProfile) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "supplier_profiles"}}
}
func (SupplierProfile) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (SupplierProfile) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id").Unique().Immutable(),
		field.Enum("status").Values("pending", "active", "rejected", "suspended").Default("pending"),
		field.String("review_reason").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Int64("agreement_version").Optional().Nillable(),
		field.Time("agreement_accepted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("payout_frozen").Default(false),
		field.String("risk_reason").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Int64("version").Default(1),
	}
}
