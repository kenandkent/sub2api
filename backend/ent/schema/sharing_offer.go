package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

type SharingOffer struct{ ent.Schema }

func (SharingOffer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "sharing_offers"}}
}
func (SharingOffer) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (SharingOffer) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("account_id").Unique().Immutable(),
		field.Int64("owner_user_id").Immutable(),
		field.Enum("review_status").Values("draft", "pending_review", "approved", "rejected", "revoked").Default("draft"),
		field.Bool("enabled").Default(false),
		field.JSON("allowed_models", []string{}).Default(func() []string { return []string{} }).SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("version").Default(1),
		field.Time("revoked_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
func (SharingOffer) Indexes() []ent.Index { return []ent.Index{index.Fields("owner_user_id")} }
